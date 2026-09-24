package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryevidence"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryrevision"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
)

// 页面大小与批次限额（docs/memory-design.md 5.2），评测后可调整。
const (
	maxTitleRunes      = 120
	maxSummaryRunes    = 300
	maxBodyBytes       = 8 << 10
	maxBatchPages      = 8
	maxAliases         = 12
	maxLinksPerPage    = 8
	maxClaimsPerPage   = 24
	maxEvidencePerPlan = 32
	maxReasonRunes     = 500
	maxCanonicalRunes  = 160
)

// ErrPlanInvalid 表示 PatchPlan 未通过确定性校验；
// 结构、权限与引用真实性问题整体拒绝，不能伪装成 noop。
var ErrPlanInvalid = errors.New("memory patch plan is invalid")

// validKinds 与 validBasis 限定模型可输出的枚举值。
var (
	validKinds = map[string]bool{"preference": true, "fact": true, "decision": true, "procedure": true, "lesson": true}
	validBasis = map[string]bool{"user_statement": true, "tool_observation": true, "document_statement": true, "synthesis": true}
	validRel   = map[string]bool{"": true, "support": true, "refute": true}
	validActs  = map[string]bool{"create": true, "update": true, "noop": true, "conflict": true}
)

// EvidenceIndex 是发布前校验的允许证据集合：本批来源片段 ∪ 目标页当前修订证据。
type EvidenceIndex struct {
	segments map[string]map[string]Segment                 // source_id -> part_key -> segment
	retained map[string]map[string]dtomemory.ClaimEvidence // page_id -> source\x00part\x00quote
}

// NewEvidenceIndex 建立本批允许证据集合；retained 在校验 update/conflict 时按页补充。
func NewEvidenceIndex(projections []*SourceProjection) *EvidenceIndex {
	index := &EvidenceIndex{
		segments: make(map[string]map[string]Segment, len(projections)),
		retained: make(map[string]map[string]dtomemory.ClaimEvidence),
	}
	for _, projection := range projections {
		parts := make(map[string]Segment, len(projection.Segments))
		for _, segment := range projection.Segments {
			parts[segment.PartKey] = segment
		}
		index.segments[projection.SourceID] = parts
	}
	return index
}

// AddRetained 把目标页当前修订的既有证据加入允许集合（保留主张可继续引用）。
func (e *EvidenceIndex) AddRetained(pageID string, rows []*ent.KaguyaMemoryEvidence) {
	set := e.retained[pageID]
	if set == nil {
		set = map[string]dtomemory.ClaimEvidence{}
		e.retained[pageID] = set
	}
	for _, row := range rows {
		set[evidenceKey(dtomemory.ClaimEvidence{SourceID: row.SourceID, PartKey: row.PartKey, Quote: row.Quote})] =
			dtomemory.ClaimEvidence{SourceID: row.SourceID, PartKey: row.PartKey, Quote: row.Quote, Relation: string(row.Relation)}
	}
}

func evidenceKey(ev dtomemory.ClaimEvidence) string {
	return ev.SourceID + "\x00" + ev.PartKey + "\x00" + ev.Quote
}

// ValidatePlanInput 是确定性校验的完整输入。
type ValidatePlanInput struct {
	SchemaVersionScope string // 期望作用域（作业范围）
	Projections        []*SourceProjection
	CandidatePages     map[string]*ent.KaguyaMemoryPage // 阶段 B 候选页面集合
	RelatedPages       map[string]*ent.KaguyaMemoryPage // RelatedIDs 引用的页面集合
	CandidateKeys      map[string]bool                  // 阶段 A 候选 key 集合
	Evidence           *EvidenceIndex
	Plan               *dtomemory.PatchPlan
}

// ValidatePlan 运行发布前确定性校验（docs/memory-design.md 7.5）。
// 这些校验只验证结构、权限与引用真实性，不能数学上保证语义真实。
func ValidatePlan(in ValidatePlanInput) error {
	if in.Plan.SchemaVersion != dtomemory.ContractSchemaVersion {
		return fmt.Errorf("%w: schema_version %d", ErrPlanInvalid, in.Plan.SchemaVersion)
	}
	if len(in.Plan.Changes) > maxBatchPages {
		return fmt.Errorf("%w: %d changes exceed limit %d", ErrPlanInvalid, len(in.Plan.Changes), maxBatchPages)
	}
	seenKeys := map[string]bool{}
	for i := range in.Plan.Changes {
		change := &in.Plan.Changes[i]
		if err := validatePatch(change, in, seenKeys); err != nil {
			return err
		}
	}
	return nil
}

func validatePatch(change *dtomemory.PagePatch, in ValidatePlanInput, seenKeys map[string]bool) error {
	if !validActs[change.Action] {
		return fmt.Errorf("%w: unknown action %q", ErrPlanInvalid, change.Action)
	}
	if change.Action == "noop" {
		return nil
	}
	for _, key := range change.CandidateKeys {
		if !in.CandidateKeys[key] {
			return fmt.Errorf("%w: unknown candidate key %q", ErrPlanInvalid, key)
		}
	}
	if err := validatePageShape(change); err != nil {
		return err
	}
	if err := validateNoSecrets(change); err != nil {
		return err
	}
	switch change.Action {
	case "create":
		if strings.TrimSpace(change.CanonicalKey) == "" || utf8.RuneCountInString(change.CanonicalKey) > maxCanonicalRunes {
			return fmt.Errorf("%w: invalid canonical_key", ErrPlanInvalid)
		}
	case "update", "conflict":
		page, ok := in.CandidatePages[change.PageID]
		if !ok {
			return fmt.Errorf("%w: page %q is not in the candidate set", ErrPlanInvalid, change.PageID)
		}
		if change.BaseVersion != page.Version {
			return fmt.Errorf("%w: page %q base_version %d does not match current version %d", ErrPlanInvalid, change.PageID, change.BaseVersion, page.Version)
		}
	}
	for _, claim := range change.Claims {
		if seenKeys[claim.Key] {
			return fmt.Errorf("%w: duplicate claim key %q", ErrPlanInvalid, claim.Key)
		}
		seenKeys[claim.Key] = true
		if claim.Key == "" || claim.Statement == "" || !validBasis[claim.Basis] {
			return fmt.Errorf("%w: invalid claim %q", ErrPlanInvalid, claim.Key)
		}
		if len(claim.Evidence) == 0 {
			return fmt.Errorf("%w: claim %q has no evidence", ErrPlanInvalid, claim.Key)
		}
		if len(claim.Evidence) > maxEvidencePerPlan {
			return fmt.Errorf("%w: claim %q has too many evidence refs", ErrPlanInvalid, claim.Key)
		}
		for _, ev := range claim.Evidence {
			if !validRel[ev.Relation] {
				return fmt.Errorf("%w: invalid evidence relation %q", ErrPlanInvalid, ev.Relation)
			}
			if !in.Evidence.allows(change.PageID, ev) {
				return fmt.Errorf("%w: evidence %q/%q is outside the allowed set", ErrPlanInvalid, ev.SourceID, ev.PartKey)
			}
		}
	}
	if len(change.RelatedIDs) > maxLinksPerPage {
		return fmt.Errorf("%w: too many related pages", ErrPlanInvalid)
	}
	for _, related := range change.RelatedIDs {
		page, ok := in.RelatedPages[related]
		if !ok {
			return fmt.Errorf("%w: related page %q does not exist", ErrPlanInvalid, related)
		}
		// 相关页面不能跨入其他项目或其他作用域。
		if page.ScopeKey != in.SchemaVersionScope {
			return fmt.Errorf("%w: related page %q crosses scopes", ErrPlanInvalid, related)
		}
	}
	return nil
}

// allows 校验证据引用：本批来源片段要求 quote 是该片段子串；
// 保留旧主张可以继续引用目标页已存在的证据记录（quote 一致）。
func (e *EvidenceIndex) allows(pageID string, ev dtomemory.ClaimEvidence) bool {
	if parts, ok := e.segments[ev.SourceID]; ok {
		segment, ok := parts[ev.PartKey]
		if !ok {
			return false
		}
		return ev.Quote != "" && strings.Contains(segment.Text, ev.Quote)
	}
	if set, ok := e.retained[pageID]; ok {
		_, ok := set[evidenceKey(ev)]
		return ok
	}
	return false
}

func validatePageShape(change *dtomemory.PagePatch) error {
	if !validKinds[change.Kind] {
		return fmt.Errorf("%w: invalid kind %q", ErrPlanInvalid, change.Kind)
	}
	if utf8.RuneCountInString(change.Title) == 0 || utf8.RuneCountInString(change.Title) > maxTitleRunes {
		return fmt.Errorf("%w: invalid title", ErrPlanInvalid)
	}
	if utf8.RuneCountInString(change.Summary) > maxSummaryRunes {
		return fmt.Errorf("%w: invalid summary", ErrPlanInvalid)
	}
	if len(change.Body) > maxBodyBytes {
		return fmt.Errorf("%w: body exceeds %d bytes", ErrPlanInvalid, maxBodyBytes)
	}
	if len(change.Aliases) > maxAliases {
		return fmt.Errorf("%w: too many aliases", ErrPlanInvalid)
	}
	for _, alias := range change.Aliases {
		if strings.TrimSpace(alias) == "" || utf8.RuneCountInString(alias) > maxTitleRunes {
			return fmt.Errorf("%w: invalid alias", ErrPlanInvalid)
		}
	}
	if utf8.RuneCountInString(change.Reason) > maxReasonRunes {
		return fmt.Errorf("%w: invalid reason", ErrPlanInvalid)
	}
	if len(change.Claims) > maxClaimsPerPage {
		return fmt.Errorf("%w: too many claims", ErrPlanInvalid)
	}
	return nil
}

// validateNoSecrets 是生成校验：明显秘密不允许由编译写入；
// 正则只能覆盖已知模式，高风险资料需人工确认。人工保存是用户明确动作，不受此限。
func validateNoSecrets(change *dtomemory.PagePatch) error {
	for _, text := range []string{change.Title, change.Summary, change.Body, change.Reason} {
		if redactSecrets(text) != text {
			return fmt.Errorf("%w: content contains secret-looking material", ErrPlanInvalid)
		}
	}
	for _, claim := range change.Claims {
		for _, ev := range claim.Evidence {
			if redactSecrets(ev.Quote) != ev.Quote {
				return fmt.Errorf("%w: evidence quote contains secret-looking material", ErrPlanInvalid)
			}
		}
	}
	return nil
}

// LoadRetainedEvidence 为候选页补充当前修订证据（保留主张允许集合）。
func LoadRetainedEvidence(ctx context.Context, client *ent.Client, index *EvidenceIndex, pages map[string]*ent.KaguyaMemoryPage) error {
	for pageID := range pages {
		revision, err := client.KaguyaMemoryRevision.Query().
			Where(kaguyamemoryrevision.PageIDEQ(pageID)).
			Order(ent.Desc(kaguyamemoryrevision.FieldVersion)).First(ctx)
		if ent.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		rows, err := client.KaguyaMemoryEvidence.Query().
			Where(kaguyamemoryevidence.RevisionIDEQ(revision.ID)).All(ctx)
		if err != nil {
			return err
		}
		index.AddRetained(pageID, rows)
	}
	return nil
}

// ValidateManualPage 校验人工保存的页面内容（复用同一限额）。
func ValidateManualPage(title, summary, body, canonicalKey string, aliases []string) error {
	change := &dtomemory.PagePatch{
		Kind: "fact", Title: title, Summary: summary, Body: body,
		Aliases: aliases, CanonicalKey: canonicalKey,
	}
	if err := validatePageShape(change); err != nil {
		return err
	}
	if strings.TrimSpace(canonicalKey) == "" || utf8.RuneCountInString(canonicalKey) > maxCanonicalRunes {
		return fmt.Errorf("%w: invalid canonical_key", ErrPlanInvalid)
	}
	return nil
}

// LoadSegmentsForSources 重建来源片段（发布前允许集合与覆盖范围计算共用）。
func LoadSegmentsForSources(ctx context.Context, client *ent.Client, ids []string) (map[string][]Segment, error) {
	out := make(map[string][]Segment, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	sources, err := client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.IDIn(ids...)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, src := range sources {
		segments, err := LoadSourceSegments(ctx, client, src)
		if err != nil {
			return nil, err
		}
		out[src.ID] = segments
	}
	return out, nil
}
