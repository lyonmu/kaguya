package memory

import (
	"fmt"
	"strings"
	"testing"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
)

func validationInput(plan *dtomemory.PatchPlan, projections []*SourceProjection) ValidatePlanInput {
	index := NewEvidenceIndex(projections)
	pages := map[string]*ent.KaguyaMemoryPage{}
	related := map[string]*ent.KaguyaMemoryPage{}
	return ValidatePlanInput{
		SchemaVersionScope: ScopePersonal,
		Projections:        projections,
		CandidatePages:     pages,
		RelatedPages:       related,
		CandidateKeys:      map[string]bool{"memory-storage": true},
		Evidence:           index,
		Plan:               plan,
	}
}

func testProjection() []*SourceProjection {
	return []*SourceProjection{{
		SourceID: "source-1", ScopeKey: ScopePersonal, PartsTotal: 1,
		Segments: []Segment{{PartKey: "user", Origin: OriginUserStatement, Text: "这个项目继续用 SQLCipher，不引入第二个数据库。"}},
	}}
}

func validChange() dtomemory.PagePatch {
	return dtomemory.PagePatch{
		Action: "create", CandidateKeys: []string{"memory-storage"},
		CanonicalKey: "memory-storage-sqlcipher", Kind: "decision",
		Title: "Memory 复用 SQLCipher", Summary: "不增加第二个数据库", Body: pageBody("SQLCipher 单后端"),
		Aliases: []string{"记忆存储"},
		Claims: []dtomemory.ClaimPatch{{
			Key: "storage", Statement: "Memory 继续使用 SQLCipher", Basis: "user_statement",
			Evidence: []dtomemory.ClaimEvidence{{
				SourceID: "source-1", PartKey: "user", Quote: "继续用 SQLCipher", Relation: "support",
			}},
		}},
		Reason: "用户明确决定",
	}
}

// 来源伪造、非子串 quote、错误 part 与越权引用全部拒绝，不能当作 noop。
func TestValidatePlanRejectsBadEvidence(t *testing.T) {
	t.Run("合法计划通过", func(t *testing.T) {
		plan := &dtomemory.PatchPlan{SchemaVersion: 1, Changes: []dtomemory.PagePatch{validChange()}}
		if err := ValidatePlan(validationInput(plan, testProjection())); err != nil {
			t.Fatal(err)
		}
	})
	cases := map[string]func(*dtomemory.PagePatch){
		"伪造来源 ID":   func(c *dtomemory.PagePatch) { c.Claims[0].Evidence[0].SourceID = "made-up" },
		"非子串 quote": func(c *dtomemory.PagePatch) { c.Claims[0].Evidence[0].Quote = "凭空引用" },
		"错误 part":   func(c *dtomemory.PagePatch) { c.Claims[0].Evidence[0].PartKey = "assistant:block-9" },
		"未知候选 key":  func(c *dtomemory.PagePatch) { c.CandidateKeys = []string{"phantom"} },
		"非法枚举":      func(c *dtomemory.PagePatch) { c.Kind = "secret" },
		"空主张证据":     func(c *dtomemory.PagePatch) { c.Claims[0].Evidence = nil },
		"秘密内容":      func(c *dtomemory.PagePatch) { c.Body += " api_key=sk-abcdefgh12345678" },
		"伪造页面 ID":   func(c *dtomemory.PagePatch) { c.Action = "update"; c.PageID = "ghost"; c.BaseVersion = 1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			change := validChange()
			mutate(&change)
			plan := &dtomemory.PatchPlan{SchemaVersion: 1, Changes: []dtomemory.PagePatch{change}}
			if err := ValidatePlan(validationInput(plan, testProjection())); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	t.Run("未知字段契约拒绝", func(t *testing.T) {
		raw := `{"schema_version":1,"candidates":[{"key":"k","kind":"fact","title":"t","statement":"s","aliases":[],"basis":"user_statement","evidence":[],"scope_key":"shared"}]}`
		var extracted dtomemory.ExtractResult
		if err := strictDecodeJSON(raw, &extracted); err == nil {
			t.Fatal("unknown field must be rejected")
		}
	})
	t.Run("尾随 JSON 拒绝", func(t *testing.T) {
		raw := extractJSON() + `{}`
		var extracted dtomemory.ExtractResult
		if err := strictDecodeJSON(raw, &extracted); err == nil {
			t.Fatal("trailing JSON must be rejected")
		}
	})
}

// update/conflict 的 ID 必须属于当前候选集且版本一致；跨作用域链接拒绝。
func TestValidatePlanCandidateBounds(t *testing.T) {
	projections := testProjection()
	page := &ent.KaguyaMemoryPage{ID: "page-1", Version: 3, ScopeKey: ScopePersonal}
	change := validChange()
	change.Action = "update"
	change.PageID = "page-1"
	change.BaseVersion = 3
	plan := &dtomemory.PatchPlan{SchemaVersion: 1, Changes: []dtomemory.PagePatch{change}}
	in := validationInput(plan, projections)
	in.CandidatePages["page-1"] = page
	if err := ValidatePlan(in); err != nil {
		t.Fatal(err)
	}

	stale := in
	staleChange := validChange()
	staleChange.Action = "update"
	staleChange.PageID = "page-1"
	staleChange.BaseVersion = 2
	stale.Plan = &dtomemory.PatchPlan{SchemaVersion: 1, Changes: []dtomemory.PagePatch{staleChange}}
	if err := ValidatePlan(stale); err == nil {
		t.Fatal("stale base_version must be rejected")
	}

	cross := in
	crossChange := validChange()
	crossChange.RelatedIDs = []string{"other-project-page"}
	cross.Plan = &dtomemory.PatchPlan{SchemaVersion: 1, Changes: []dtomemory.PagePatch{crossChange}}
	cross.RelatedPages = map[string]*ent.KaguyaMemoryPage{
		"other-project-page": {ID: "other-project-page", ScopeKey: "project:p-2"},
	}
	if err := ValidatePlan(cross); err == nil {
		t.Fatal("cross-scope related page must be rejected")
	}

	// 知识页数量由模型决定；超过旧的八页阈值仍可发布。
	many := &dtomemory.PatchPlan{SchemaVersion: 1}
	for i := 0; i < 12; i++ {
		change := validChange()
		change.CanonicalKey = fmt.Sprintf("topic-%d", i)
		many.Changes = append(many.Changes, change)
	}
	if err := ValidatePlan(validationInput(many, projections)); err != nil {
		t.Fatal(err)
	}
}

// 保留旧主张只能引用目标页已存在证据或本批新证据，不能编造历史来源。
func TestValidatePlanRetainedEvidenceOnly(t *testing.T) {
	projections := testProjection()
	page := &ent.KaguyaMemoryPage{ID: "page-1", Version: 3, ScopeKey: ScopePersonal}
	change := validChange()
	change.Action = "update"
	change.PageID = "page-1"
	change.BaseVersion = 3
	change.Claims[0].Evidence = append(change.Claims[0].Evidence, dtomemory.ClaimEvidence{
		SourceID: "old-source", PartKey: "user", Quote: "历史引用", Relation: "support",
	})
	plan := &dtomemory.PatchPlan{SchemaVersion: 1, Changes: []dtomemory.PagePatch{change}}
	in := validationInput(plan, projections)
	in.CandidatePages["page-1"] = page
	if err := ValidatePlan(in); err == nil {
		t.Fatal("invented historical evidence must be rejected")
	}
	in.Evidence.AddRetained("page-1", []*ent.KaguyaMemoryEvidence{{
		SourceID: "old-source", PartKey: "user", Quote: "历史引用", Basis: "user_statement",
	}})
	if err := ValidatePlan(in); err != nil {
		t.Fatalf("retained evidence rejected: %v", err)
	}
}

// 手工保存复用同一限额；人工允许保存自己的秘密（用户明确动作），
// 编译写入秘密仍被拒绝。
func TestManualValidationDiffersFromCompiler(t *testing.T) {
	if err := ValidateManualPage("标题", "", "api_key=sk-abcdefgh12345678", "key", nil); err != nil {
		t.Fatalf("manual save must accept user content: %v", err)
	}
	change := validChange()
	change.Body = "api_key=sk-abcdefgh12345678"
	if err := validateNoSecrets(&change); err == nil {
		t.Fatal("compiler output containing secrets must be rejected")
	}
	if err := ValidateManualPage("标题", "", "body", strings.Repeat("k", maxCanonicalBytes+1), nil); err == nil {
		t.Fatal("manual save must enforce canonical_key limit")
	}
}
