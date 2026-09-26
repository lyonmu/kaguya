package memory

import (
	"errors"
	"reflect"
	"testing"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
)

// 版本对比：任意两个修订、前一个版本、内容/元数据/主张/证据变化都稳定可解释。
func TestCompareRevisionsArbitraryVersions(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	page, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: ScopePersonal, Kind: "decision", CanonicalKey: "storage",
		Title: "存储决定", Summary: "使用 SQLCipher", Body: "旧正文",
		Aliases: []string{"数据库"},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.UpdatePage(ctx, page.ID, &dtomemory.MemoryPageUpdateReq{
		ExpectedVersion: page.Version, Kind: "procedure", Title: "存储决定 v2",
		Summary: ptrString("使用 SQLCipher 与 FTS5"), Body: ptrString("新正文"), Aliases: []string{"数据库", "检索"},
		Reason: "补充检索细节",
	})
	if err != nil || updated.Version != 2 {
		t.Fatalf("update=%+v err=%v", updated, err)
	}
	restored, err := svc.RestoreRevision(ctx, []string{ScopePersonal}, page.ID, 1)
	if err != nil || restored.Version != 3 {
		t.Fatalf("restore=%+v err=%v", restored, err)
	}

	diff, err := svc.CompareRevisions(ctx, []string{ScopePersonal}, page.ID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if diff.From.Version != 1 || diff.To.Version != 2 || diff.To.Reason != "补充检索细节" {
		t.Fatalf("sides=%+v", diff)
	}
	fields := map[string]string{}
	for _, change := range diff.ContentChanges {
		fields[change.Field] = change.To
	}
	if fields["title"] != "存储决定 v2" || fields["body"] != "新正文" {
		t.Fatalf("content changes=%+v", diff.ContentChanges)
	}
	meta := map[string]string{}
	for _, change := range diff.MetadataChanges {
		meta[change.Field] = change.To
	}
	if meta["kind"] != "procedure" || meta["aliases"] != "数据库、检索" {
		t.Fatalf("metadata changes=%+v", diff.MetadataChanges)
	}
	if len(diff.ClaimChanges) != 1 || diff.ClaimChanges[0].Change != "changed" {
		t.Fatalf("claim changes=%+v", diff.ClaimChanges)
	}

	// 恢复 v1 后与 v2 对比：正文回到旧值。
	back, err := svc.CompareRevisions(ctx, []string{ScopePersonal}, page.ID, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range back.ContentChanges {
		if change.Field == "body" && change.To != "旧正文" {
			t.Fatalf("restored body=%+v", change)
		}
	}
	// from=0 表示 to 的前一个版本。
	previous, err := svc.CompareRevisions(ctx, []string{ScopePersonal}, page.ID, 0, 3)
	if err != nil || previous.From.Version != 2 {
		t.Fatalf("previous=%+v err=%v", previous, err)
	}
	// 对比结果确定性：重复调用完全一致。
	again, err := svc.CompareRevisions(ctx, []string{ScopePersonal}, page.ID, 1, 2)
	if err != nil || !reflect.DeepEqual(diff, again) {
		t.Fatalf("diff not deterministic: %+v vs %+v err=%v", diff, again, err)
	}
}

// 主张与证据级变化：added / removed / changed 与来源引用都可解释。
func TestCompareRevisionsClaimAndEvidenceChanges(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	page, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: ScopePersonal, Kind: "fact", CanonicalKey: "topic",
		Title: "主题", Summary: "摘要", Body: "正文",
	})
	if err != nil {
		t.Fatal(err)
	}
	note, err := client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.SourceKeyEQ("note:" + page.ID)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 直接构造 v2：保留 note 主张但改变陈述，并新增一条带证据的主张。
	page2, err := client.KaguyaMemoryPage.UpdateOneID(page.ID).
		SetBody("正文 v2").SetVersion(page.Version + 1).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	change := &dtomemory.PagePatch{
		Kind: "fact", Title: "主题", Summary: "摘要", Body: "正文 v2",
		Claims: []dtomemory.ClaimPatch{
			{Key: manualClaimKey, Statement: "改写的陈述", Basis: "user_statement",
				Evidence: []dtomemory.ClaimEvidence{{SourceID: note.ID, PartKey: "note", Quote: "正文 v2", Relation: "support"}}},
			{Key: "extra", Statement: "新增主张", Basis: "synthesis",
				Evidence: []dtomemory.ClaimEvidence{{SourceID: note.ID, PartKey: "note", Quote: "正文", Relation: "support"}}},
		},
	}
	if err := saveRevisionTx(ctx, client, page2, change, actorUser, ""); err != nil {
		t.Fatal(err)
	}
	diff, err := svc.CompareRevisions(ctx, []string{ScopePersonal}, page.ID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	claimChanges := map[string]string{}
	for _, item := range diff.ClaimChanges {
		claimChanges[item.Key] = item.Change
	}
	if claimChanges[manualClaimKey] != "changed" || claimChanges["extra"] != "added" {
		t.Fatalf("claim changes=%+v", diff.ClaimChanges)
	}
	// note 主张的旧证据被替换（removed + added），extra 主张新增证据。
	if len(diff.EvidenceChanges) != 3 {
		t.Fatalf("evidence changes=%+v", diff.EvidenceChanges)
	}
	changes := map[string]string{}
	for _, item := range diff.EvidenceChanges {
		if item.SourceID != note.ID {
			t.Fatalf("evidence change=%+v", item)
		}
		changes[item.ClaimKey+"|"+item.Quote] = item.Change
	}
	if changes["extra|正文"] != "added" || changes[manualClaimKey+"|正文 v2"] != "added" || changes[manualClaimKey+"|正文"] != "removed" {
		t.Fatalf("evidence changes=%+v", diff.EvidenceChanges)
	}
}

// 版本不存在与越权访问被拒绝。
func TestCompareRevisionsErrors(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	page, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: ScopePersonal, Kind: "fact", Title: "错误路径", Body: "正文",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompareRevisions(ctx, []string{ScopePersonal}, page.ID, 1, 99); !errors.Is(err, ErrPageVersionGone) {
		t.Fatalf("missing version err=%v", err)
	}
	if _, err := svc.CompareRevisions(ctx, []string{ScopeShared}, page.ID, 1, 1); !errors.Is(err, ErrPageForbidden) {
		t.Fatalf("cross scope err=%v", err)
	}
	if _, err := svc.CompareRevisions(ctx, []string{ScopePersonal}, "missing", 1, 1); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("missing page err=%v", err)
	}
	// from=0 且只有一个版本时没有前一个版本。
	if _, err := svc.CompareRevisions(ctx, []string{ScopePersonal}, page.ID, 0, 1); !errors.Is(err, ErrPageVersionGone) {
		t.Fatalf("no previous err=%v", err)
	}
}
