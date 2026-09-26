package memory

import (
	"testing"
	"time"

	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorypage"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryrevision"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
)

// 捕获：completed 入队；running/failed/canceled/interrupted 不入队；
// 重复捕获由唯一约束去重；会话记忆模式与全局开关生效。
func TestCaptureCompletedTurnOutbox(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	conv := makeConversation(t, ctx, client, "conv-1", "", kaguyaconversation.MemoryModeInherit)
	turnID := makeTurn(t, ctx, client, conv, "这个项目继续用 SQLCipher，不引入第二个数据库。", "好的，记录该约束。")
	captureTurn(t, ctx, client, conv, turnID)

	sources, err := client.KaguyaMemorySource.Query().All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources=%d", len(sources))
	}
	src := sources[0]
	if src.SourceKey != "turn:"+turnID+":projection-v1" || src.State != kaguyamemorysource.StatePending ||
		src.ScopeKey != ScopePersonal {
		t.Fatalf("source=%+v", src)
	}
	// 轻量 Outbox：捕获事务不读来源投影，内容哈希留给 Worker 异步推导。
	if src.ContentHash != "" {
		t.Fatalf("capture must not read the source projection: %+v", src)
	}
	backdateSources(t, ctx, client, 2*time.Minute)
	if job, _ := NewWorker(svc).claimReadyBatch(ctx, ""); job == nil {
		t.Fatal("expected claimed job")
	} else {
		derived, err := client.KaguyaMemorySource.Get(ctx, src.ID)
		if err != nil || derived.ContentHash == "" {
			t.Fatalf("worker did not derive source hash: %+v err=%v", derived, err)
		}
	}
	// 已领取来源不重复入队，回退为待处理以便后续断言计数。
	if err := client.KaguyaMemorySource.Update().
		SetState(kaguyamemorysource.StatePending).SetJobID("").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	// 重复捕获不重复入队。
	captureTurn(t, ctx, client, conv, turnID)
	if count, err := client.KaguyaMemorySource.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("duplicate capture: count=%d err=%v", count, err)
	}

	// 未完成轮次不入队。
	runningID, err := client.KaguyaChatTurn.Create().
		SetConversationID(conv).SetTurnIndex(2).SetStatus(kaguyachatturn.StatusFailed).
		SetUserContent("失败轮次").
		SetProviderID("p").SetProviderName("p").SetModelID("m").SetModelName("m").SetAPIProtocol("openai-chat").
		SetStartedAt(time.Now()).SetFinishedAt(time.Now()).SetDurationMs(1).SetToolCalls(0).
		SetFinishReason("error").SetInputTokens(0).SetOutputTokens(0).SetTotalTokens(0).
		SetCachedTokens(0).SetReasoningTokens(0).SetMessages(nil).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	captureTurn(t, ctx, client, conv, runningID.ID)
	if count, err := client.KaguyaMemorySource.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("incomplete turn captured: count=%d err=%v", count, err)
	}

	// 只读与关闭模式的会话都不自动捕获。
	for _, mode := range []kaguyaconversation.MemoryMode{kaguyaconversation.MemoryModeOff, kaguyaconversation.MemoryModeReadonly} {
		other := makeConversation(t, ctx, client, "conv-"+string(mode), "", mode)
		otherTurn := makeTurn(t, ctx, client, other, "私密内容")
		captureTurn(t, ctx, client, other, otherTurn)
	}
	if count, err := client.KaguyaMemorySource.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("private conversation captured: count=%d err=%v", count, err)
	}

	// 关闭自动捕获后不再产生新来源。
	setupPolicy(t, ctx, client, true, false)
	late := makeConversation(t, ctx, client, "conv-late", "", kaguyaconversation.MemoryModeInherit)
	lateTurn := makeTurn(t, ctx, client, late, "关闭捕获后的提问")
	captureTurn(t, ctx, client, late, lateTurn)
	if count, err := client.KaguyaMemorySource.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("auto capture off: count=%d err=%v", count, err)
	}
}

// 项目会话来源落在项目作用域；项目删除后不再摄取。
func TestCaptureScopeFollowsConversationProject(t *testing.T) {
	ctx, _, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	conv := makeConversation(t, ctx, client, "conv-project", projectID, kaguyaconversation.MemoryModeInherit)
	turnID := makeTurn(t, ctx, client, conv, "项目约束")
	captureTurn(t, ctx, client, conv, turnID)
	src, err := client.KaguyaMemorySource.Query().Only(ctx)
	if err != nil || src.ScopeKey != "project:"+projectID {
		t.Fatalf("source=%+v err=%v", src, err)
	}
	if err := client.KaguyaProject.UpdateOneID(projectID).SetDeletedAt(time.Now()).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	turn2 := makeTurn(t, ctx, client, conv, "删除项目后的提问")
	captureTurn(t, ctx, client, conv, turn2)
	if count, err := client.KaguyaMemorySource.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("project deleted still captured: count=%d err=%v", count, err)
	}
}

// step_limit 的 completed 轮次照常入队，但投影保留 finish_reason，
// 编译必须保留未确定性，不把“轨迹完整”当作“任务已完成”。
func TestCaptureKeepsStepLimitUncertainty(t *testing.T) {
	ctx, _, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	conv := makeConversation(t, ctx, client, "conv-step", "", kaguyaconversation.MemoryModeInherit)
	turnID := makeTurn(t, ctx, client, conv, "尝试修复打包", "已尝试，但只跑到步骤上限")
	row, err := client.KaguyaChatTurn.UpdateOneID(turnID).SetFinishReason("step_limit").Save(ctx)
	if err != nil || row.FinishReason != "step_limit" {
		t.Fatalf("turn=%+v err=%v", row, err)
	}
	captureTurn(t, ctx, client, conv, turnID)
	src, err := client.KaguyaMemorySource.Query().Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := LoadSourceProjection(ctx, client, src)
	if err != nil {
		t.Fatal(err)
	}
	if projection.FinishReason != "step_limit" {
		t.Fatalf("projection finish=%q", projection.FinishReason)
	}
}

// 人工保存的完整生命周期：保存、编辑冲突、停用、修订恢复与遗忘清除。
func TestManualPageLifecycle(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	conv := makeConversation(t, ctx, client, "conv-manual", projectID, kaguyaconversation.MemoryModeInherit)
	turnID := makeTurn(t, ctx, client, conv, "这个项目继续用 SQLCipher，不引入第二个数据库。")

	scope := "project:" + projectID
	detail, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: scope, Kind: "decision",
		Title: "Memory 使用现有 SQLCipher", Summary: "不新增第二个数据库",
		Body:    pageBody("Memory 与聊天数据共享 SQLCipher"),
		Aliases: []string{"记忆存储"},
		Source:  &dtomemory.MemorySourceRefReq{ConversationID: conv, TurnID: turnID, PartKey: "user", Quote: "继续用 SQLCipher"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != "active" || detail.Version != 1 || len(detail.Claims) == 0 {
		t.Fatalf("detail=%+v", detail)
	}
	// 来源展示：笔记自证 + 真实轮次证据。
	if detail.SourceCount < 2 {
		t.Fatalf("sources=%d", detail.SourceCount)
	}
	if count, err := client.KaguyaMemorySearchDoc.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("search doc: count=%d err=%v", count, err)
	}

	// 人工并发编辑返回明确冲突。
	summary := "更新后的摘要"
	if _, err := svc.UpdatePage(ctx, detail.ID, &dtomemory.MemoryPageUpdateReq{
		ExpectedVersion: 1, Summary: &summary,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdatePage(ctx, detail.ID, &dtomemory.MemoryPageUpdateReq{
		ExpectedVersion: 1, Summary: &summary,
	}); err != ErrPageVersionConflict {
		t.Fatalf("expected version conflict, got %v", err)
	}

	// 停用：不再召回，可恢复，内容保留。
	if err := svc.DeletePage(ctx, detail.ID, "disable"); err != nil {
		t.Fatal(err)
	}
	page, err := client.KaguyaMemoryPage.Get(ctx, detail.ID)
	if err != nil || page.Status != kaguyamemorypage.StatusArchived || page.Version != 3 {
		t.Fatalf("disabled page=%+v err=%v", page, err)
	}
	if count, err := client.KaguyaMemorySearchDoc.Query().Count(ctx); err != nil || count != 0 {
		t.Fatalf("archived page still indexed: count=%d err=%v", count, err)
	}

	// 恢复旧修订：新建修订，不回退版本号。
	restored, err := svc.RestoreRevision(ctx, []string{scope}, detail.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Version != 4 || restored.Title != "Memory 使用现有 SQLCipher" || restored.Status != "active" {
		t.Fatalf("restored=%+v", restored)
	}
	if count, err := client.KaguyaMemorySearchDoc.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("restored page index: count=%d err=%v", count, err)
	}

	// 遗忘：撤出索引，清除正文/修订/证据摘录，保留 tombstone，读取不可见。
	if err := svc.DeletePage(ctx, detail.ID, "forget"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReadPageDetail(ctx, []string{scope}, detail.ID, 0); err != ErrPageNotFound {
		t.Fatalf("forgotten page readable: %v", err)
	}
	if _, err := svc.ReadPageDetail(ctx, []string{scope}, detail.ID, 1); err != ErrPageNotFound {
		t.Fatalf("forgotten revision readable: %v", err)
	}
	tombstone, err := client.KaguyaMemoryPage.Get(ctx, detail.ID)
	if err != nil || tombstone.Status != kaguyamemorypage.StatusDeleted || tombstone.Body != "" {
		t.Fatalf("tombstone=%+v err=%v", tombstone, err)
	}
	if count, err := client.KaguyaMemoryRevision.Query().Count(ctx); err != nil || count != 0 {
		t.Fatalf("revisions kept: count=%d err=%v", count, err)
	}
	if revisions, err := svc.ListRevisions(ctx, []string{scope}, detail.ID); err == nil && len(revisions) != 0 {
		t.Fatalf("revision history bypassed deletion: %+v", revisions)
	}
}

// tombstone 阻止编译自动复活；用户显式重建同主题时复活为新修订。
func TestTombstoneBlocksAutoResurrection(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	detail, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: ScopePersonal, Kind: "fact", Title: "部署方式", Body: "旧部署方式",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeletePage(ctx, detail.ID, "forget"); err != nil {
		t.Fatal(err)
	}
	// 编译侧：同 key 的 create 被放弃，不复活。
	revived, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: ScopePersonal, Kind: "fact", Title: "部署方式", Body: "新的部署方式",
	})
	if err != nil {
		t.Fatal(err)
	}
	if revived.ID != detail.ID || revived.Version != detail.Version+2 {
		t.Fatalf("revive=%+v want same id", revived)
	}
	if revived.Body != "新的部署方式" {
		t.Fatalf("revive body=%q", revived.Body)
	}
	// 复活产生新的用户修订。
	revisions, err := svc.ListRevisions(ctx, []string{ScopePersonal}, detail.ID)
	if err != nil || len(revisions) != 1 || revisions[0].Actor != "user" {
		t.Fatalf("revisions=%+v err=%v", revisions, err)
	}
}

// 确定性完整性检查：孤立关系清理并统计已撤销来源、过期页面与可疑重复。
func TestLintPassDeterministicChecks(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	kept, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: ScopePersonal, Kind: "fact", Title: "保留页面", Body: "内容 A",
	})
	if err != nil {
		t.Fatal(err)
	}
	expired, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: ScopePersonal, Kind: "fact", Title: "过期页面", Body: "内容 B",
	})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := client.KaguyaMemoryPage.UpdateOneID(expired.ID).SetExpiresAt(past).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	forgotten, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: ScopePersonal, Kind: "fact", Title: "将被遗忘", Body: "内容 C",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeletePage(ctx, forgotten.ID, "forget"); err != nil {
		t.Fatal(err)
	}
	// 指向已删除页面的孤立关系。
	if err := client.KaguyaMemoryLink.Create().
		SetFromPageID(kept.ID).SetToPageID(forgotten.ID).SetRelation("related").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	// 已排除来源的证据残留。
	if err := client.KaguyaMemorySource.Create().
		SetSourceKey("turn:gone:projection-v1").SetKind("turn").SetScopeKey(ScopePersonal).
		SetState("excluded").SetCapturedAt(time.Now()).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	excluded, err := client.KaguyaMemorySource.Query().Where(kaguyamemorysource.SourceKeyEQ("turn:gone:projection-v1")).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := client.KaguyaMemoryRevision.Query().
		Where(kaguyamemoryrevision.PageIDEQ(kept.ID)).First(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaMemoryEvidence.Create().
		SetRevisionID(revision.ID).SetClaimKey("extra").SetSourceID(excluded.ID).
		SetPartKey("user").SetQuote("x").SetRelation("support").SetBasis("user_statement").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	// 同范围同标题的可疑重复。
	if _, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: ScopePersonal, Kind: "fact", Title: "保留页面", CanonicalKey: "dup-1", Body: "内容 D",
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := svc.LintPass(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.OrphanLinks != 1 || stats.ExcludedEvidence < 1 || stats.ExpiredPages < 1 || stats.DuplicateTitles < 1 {
		t.Fatalf("stats=%+v", stats)
	}
	// 孤立关系被清理，其他只提示不擅自改内容。
	if count, err := client.KaguyaMemoryLink.Query().Count(ctx); err != nil || count != 0 {
		t.Fatalf("orphan links kept: count=%d err=%v", count, err)
	}
}
