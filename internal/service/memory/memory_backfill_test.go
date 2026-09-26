package memory

import (
	"fmt"
	"testing"
	"time"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryjob"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
)

// runBackfillToCompletion 反复领取回填作业直到没有可领取的作业。
func runBackfillToCompletion(t *testing.T, svc *Service, maxRounds int) {
	t.Helper()
	worker := NewWorker(svc)
	for i := 0; i < maxRounds; i++ {
		job := worker.claimBackfillJob(t.Context())
		if job == nil {
			return
		}
		svc.runBackfill(t.Context(), job)
	}
	t.Fatal("backfill did not finish within round budget")
}

// 回填分页 + checkpoint + 幂等：首次扫描全部轮次，重跑只跳过不重复。
func TestBackfillCheckpointAndIdempotency(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	conv := makeConversation(t, ctx, client, "conv-backfill", projectID, kaguyaconversation.MemoryModeInherit)
	for i := 0; i < backfillPageSize+50; i++ {
		makeTurn(t, ctx, client, conv, fmt.Sprintf("历史问题 %d", i))
	}
	resp, err := svc.StartBackfill(ctx, &dtomemory.MemoryBackfillReq{ScopeKey: ScopeKey(projectID), MaxSources: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if resp.JobID == "" || resp.Finished {
		t.Fatalf("start=%+v", resp)
	}
	runBackfillToCompletion(t, svc, 10)
	job, err := client.KaguyaMemoryJob.Get(ctx, resp.JobID)
	if err != nil || job.Status != kaguyamemoryjob.StatusSucceeded {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	count, err := client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.ScopeKeyEQ(ScopeKey(projectID))).Count(ctx)
	if err != nil || count != backfillPageSize+50 {
		t.Fatalf("sources=%d err=%v", count, err)
	}
	progress, err := svc.backfillProgress(job)
	if err != nil || progress.Scanned != backfillPageSize+50 || progress.Created != backfillPageSize+50 ||
		progress.Skipped != 0 || !progress.Finished {
		t.Fatalf("progress=%+v err=%v", progress, err)
	}
	// 重复执行：新作业重新扫描，但已处理数据全部跳过，不产生重复来源。
	again, err := svc.StartBackfill(ctx, &dtomemory.MemoryBackfillReq{ScopeKey: ScopeKey(projectID), MaxSources: 1000})
	if err != nil {
		t.Fatal(err)
	}
	runBackfillToCompletion(t, svc, 10)
	job2, err := client.KaguyaMemoryJob.Get(ctx, again.JobID)
	if err != nil {
		t.Fatal(err)
	}
	progress2, err := svc.backfillProgress(job2)
	if err != nil || progress2.Created != 0 || progress2.Skipped != backfillPageSize+50 {
		t.Fatalf("second progress=%+v err=%v", progress2, err)
	}
	if total, err := client.KaguyaMemorySource.Query().Count(ctx); err != nil || total != backfillPageSize+50 {
		t.Fatalf("total sources=%d err=%v", total, err)
	}
}

// 成本上限：达到 max_sources 后停止并标记 limited，重跑可以继续处理剩余轮次。
func TestBackfillRespectsCostLimit(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	conv := makeConversation(t, ctx, client, "conv-limit", projectID, kaguyaconversation.MemoryModeInherit)
	for i := 0; i < 5; i++ {
		makeTurn(t, ctx, client, conv, fmt.Sprintf("问题 %d", i))
	}
	resp, err := svc.StartBackfill(ctx, &dtomemory.MemoryBackfillReq{ScopeKey: ScopeKey(projectID), MaxSources: 2})
	if err != nil {
		t.Fatal(err)
	}
	runBackfillToCompletion(t, svc, 5)
	job, err := client.KaguyaMemoryJob.Get(ctx, resp.JobID)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := svc.backfillProgress(job)
	if err != nil || progress.Created != 2 || !progress.Limited || !progress.Finished {
		t.Fatalf("progress=%+v err=%v", progress, err)
	}
	if count, err := client.KaguyaMemorySource.Query().Count(ctx); err != nil || count != 2 {
		t.Fatalf("sources=%d err=%v", count, err)
	}
	// 提高上限后继续：跳过已存在来源，补齐剩余。
	resp2, err := svc.StartBackfill(ctx, &dtomemory.MemoryBackfillReq{ScopeKey: ScopeKey(projectID), MaxSources: 10})
	if err != nil {
		t.Fatal(err)
	}
	runBackfillToCompletion(t, svc, 5)
	job2, err := client.KaguyaMemoryJob.Get(ctx, resp2.JobID)
	if err != nil {
		t.Fatal(err)
	}
	progress2, err := svc.backfillProgress(job2)
	if err != nil || progress2.Created != 3 || progress2.Skipped != 2 || progress2.Limited {
		t.Fatalf("second progress=%+v err=%v", progress2, err)
	}
	if count, err := client.KaguyaMemorySource.Query().Count(ctx); err != nil || count != 5 {
		t.Fatalf("sources=%d err=%v", count, err)
	}
}

// 私密/只读/已删除会话与已删除项目不参与回填。
func TestBackfillSkipsPrivateAndDeleted(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	allowed := makeConversation(t, ctx, client, "conv-allowed", projectID, kaguyaconversation.MemoryModeInherit)
	makeTurn(t, ctx, client, allowed, "允许回填")
	for _, mode := range []kaguyaconversation.MemoryMode{kaguyaconversation.MemoryModeOff, kaguyaconversation.MemoryModeReadonly} {
		private := makeConversation(t, ctx, client, "conv-"+string(mode), projectID, mode)
		makeTurn(t, ctx, client, private, "私密内容")
	}
	deleted := makeConversation(t, ctx, client, "conv-deleted", projectID, kaguyaconversation.MemoryModeInherit)
	makeTurn(t, ctx, client, deleted, "已删除会话内容")
	if err := client.KaguyaConversation.UpdateOneID(deleted).SetDeletedAt(time.Now()).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.StartBackfill(ctx, &dtomemory.MemoryBackfillReq{ScopeKey: ScopeKey(projectID), MaxSources: 100})
	if err != nil {
		t.Fatal(err)
	}
	runBackfillToCompletion(t, svc, 5)
	sources, err := client.KaguyaMemorySource.Query().All(ctx)
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	if sources[0].ScopeKey != ScopeKey(projectID) || sources[0].ConversationID != allowed {
		t.Fatalf("source=%+v", sources[0])
	}
	job, err := client.KaguyaMemoryJob.Get(ctx, resp.JobID)
	if err != nil {
		t.Fatal(err)
	}
	progress, _ := svc.backfillProgress(job)
	if progress.Scanned != 1 || progress.Created != 1 {
		t.Fatalf("progress=%+v", progress)
	}
}

// 范围隔离：personal 回填不捕获项目轮次，项目回填不捕获普通对话。
func TestBackfillScopeIsolation(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	personal := makeConversation(t, ctx, client, "conv-personal", "", kaguyaconversation.MemoryModeInherit)
	makeTurn(t, ctx, client, personal, "个人对话")
	project := makeConversation(t, ctx, client, "conv-project", projectID, kaguyaconversation.MemoryModeInherit)
	makeTurn(t, ctx, client, project, "项目对话")

	resp, err := svc.StartBackfill(ctx, &dtomemory.MemoryBackfillReq{ScopeKey: ScopePersonal, MaxSources: 10})
	if err != nil {
		t.Fatal(err)
	}
	runBackfillToCompletion(t, svc, 5)
	personalSources, err := client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.ScopeKeyEQ(ScopePersonal)).All(ctx)
	if err != nil || len(personalSources) != 1 || personalSources[0].ConversationID != personal {
		t.Fatalf("personal sources=%+v err=%v", personalSources, err)
	}
	job, err := client.KaguyaMemoryJob.Get(ctx, resp.JobID)
	if err != nil {
		t.Fatal(err)
	}
	progress, _ := svc.backfillProgress(job)
	if progress.Scanned != 1 {
		t.Fatalf("personal progress=%+v", progress)
	}

	projectResp, err := svc.StartBackfill(ctx, &dtomemory.MemoryBackfillReq{ScopeKey: ScopeKey(projectID), MaxSources: 10})
	if err != nil {
		t.Fatal(err)
	}
	runBackfillToCompletion(t, svc, 5)
	projectSources, err := client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.ScopeKeyEQ(ScopeKey(projectID))).All(ctx)
	if err != nil || len(projectSources) != 1 || projectSources[0].ConversationID != project {
		t.Fatalf("project sources=%+v err=%v", projectSources, err)
	}
	if _, err := client.KaguyaMemoryJob.Get(ctx, projectResp.JobID); err != nil {
		t.Fatal(err)
	}
}

// 回填进度与任务列表：跨页时 cursor 推进、进度可见，完成后 finished=true。
func TestBackfillProgressVisibleInJobs(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	conv := makeConversation(t, ctx, client, "conv-progress", projectID, kaguyaconversation.MemoryModeInherit)
	for i := 0; i < backfillPageSize+1; i++ {
		makeTurn(t, ctx, client, conv, fmt.Sprintf("问题 %d", i))
	}
	resp, err := svc.StartBackfill(ctx, &dtomemory.MemoryBackfillReq{ScopeKey: ScopeKey(projectID), MaxSources: 1000})
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(svc)
	first := worker.claimBackfillJob(ctx)
	if first == nil {
		t.Fatal("missing first page job")
	}
	svc.runBackfill(ctx, first)
	// 第一页后作业回到 retry_wait，进度已持久化。
	mid, err := client.KaguyaMemoryJob.Get(ctx, resp.JobID)
	if err != nil || mid.Status != kaguyamemoryjob.StatusRetryWait {
		t.Fatalf("job=%+v err=%v", mid, err)
	}
	list, err := svc.ListJobs(ctx, &dtomemory.MemoryJobListReq{Page: 1, PageSize: 10})
	if err != nil || len(list.Items) != 1 || list.Items[0].Progress == nil {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	if list.Items[0].Progress.Scanned != backfillPageSize || list.Items[0].Progress.Finished {
		t.Fatalf("progress=%+v", list.Items[0].Progress)
	}
	runBackfillToCompletion(t, svc, 5)
	final, err := client.KaguyaMemoryJob.Get(ctx, resp.JobID)
	if err != nil || final.Status != kaguyamemoryjob.StatusSucceeded {
		t.Fatalf("final=%+v err=%v", final, err)
	}
	progress, _ := svc.backfillProgress(final)
	if progress.Scanned != backfillPageSize+1 || !progress.Finished {
		t.Fatalf("final progress=%+v", progress)
	}
}

// 时间范围与单会话过滤：只回填指定区间与指定会话。
func TestBackfillTimeRangeAndConversationFilter(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	convA := makeConversation(t, ctx, client, "conv-a", projectID, kaguyaconversation.MemoryModeInherit)
	convB := makeConversation(t, ctx, client, "conv-b", projectID, kaguyaconversation.MemoryModeInherit)
	oldTurn := makeTurn(t, ctx, client, convA, "旧轮次")
	newTurn := makeTurn(t, ctx, client, convB, "新轮次")
	if err := client.KaguyaChatTurn.UpdateOneID(oldTurn).
		SetFinishedAt(time.Now().Add(-48 * time.Hour)).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaChatTurn.UpdateOneID(newTurn).
		SetFinishedAt(time.Now()).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	after := time.Now().Add(-24 * time.Hour)
	resp, err := svc.StartBackfill(ctx, &dtomemory.MemoryBackfillReq{
		ScopeKey: ScopeKey(projectID), ConversationID: convB, After: &after, MaxSources: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	runBackfillToCompletion(t, svc, 5)
	job, err := client.KaguyaMemoryJob.Get(ctx, resp.JobID)
	if err != nil {
		t.Fatal(err)
	}
	progress, _ := svc.backfillProgress(job)
	if progress.Created != 1 || progress.Scanned != 1 {
		t.Fatalf("progress=%+v", progress)
	}
	src, err := client.KaguyaMemorySource.Query().Only(ctx)
	if err != nil || src.TurnID != newTurn || src.ConversationID != convB {
		t.Fatalf("source=%+v err=%v", src, err)
	}
}

// 进程崩溃后的过期租约由下次启动/扫描回收，重跑幂等不重复来源。
func TestBackfillRecoversExpiredLease(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	conv := makeConversation(t, ctx, client, "conv-crash", projectID, kaguyaconversation.MemoryModeInherit)
	makeTurn(t, ctx, client, conv, "崩溃恢复")
	resp, err := svc.StartBackfill(ctx, &dtomemory.MemoryBackfillReq{ScopeKey: ScopeKey(projectID), MaxSources: 10})
	if err != nil {
		t.Fatal(err)
	}
	// 模拟强杀：作业停在 running，租约已过期且没有持有者。
	if err := client.KaguyaMemoryJob.UpdateOneID(resp.JobID).
		SetStatus(kaguyamemoryjob.StatusRunning).SetAttempt(1).
		SetLeaseToken("stale-worker").SetLeaseExpiresAt(time.Now().Add(-time.Hour)).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(svc)
	job := worker.claimBackfillJob(ctx)
	if job == nil {
		t.Fatal("expired backfill lease was not recovered")
	}
	svc.runBackfill(ctx, job)
	runBackfillToCompletion(t, svc, 5)
	final, err := client.KaguyaMemoryJob.Get(ctx, resp.JobID)
	if err != nil || final.Status != kaguyamemoryjob.StatusSucceeded {
		t.Fatalf("final=%+v err=%v", final, err)
	}
	progress, _ := svc.backfillProgress(final)
	if progress.Created != 1 || progress.Scanned != 1 {
		t.Fatalf("progress=%+v", progress)
	}
	if count, err := client.KaguyaMemorySource.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("sources=%d err=%v", count, err)
	}
}

// 回填输入校验与关闭开关：shared、缺失项目、关闭记忆都明确拒绝。
func TestBackfillRejectsInvalidInput(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	if _, err := svc.StartBackfill(ctx, &dtomemory.MemoryBackfillReq{ScopeKey: ScopeShared, MaxSources: 1}); err == nil {
		t.Fatal("shared scope must be rejected")
	}
	if _, err := svc.StartBackfill(ctx, &dtomemory.MemoryBackfillReq{ScopeKey: "project:missing", MaxSources: 1}); err == nil {
		t.Fatal("missing project must be rejected")
	}
	projectID := makeProject(t, ctx, client, "p1")
	conv := makeConversation(t, ctx, client, "conv-other", "", kaguyaconversation.MemoryModeInherit)
	makeTurn(t, ctx, client, conv, "个人内容")
	if _, err := svc.StartBackfill(ctx, &dtomemory.MemoryBackfillReq{
		ScopeKey: ScopeKey(projectID), ConversationID: conv, MaxSources: 1,
	}); err == nil {
		t.Fatal("conversation outside scope must be rejected")
	}
	setupPolicy(t, ctx, client, false, false)
	if _, err := svc.StartBackfill(ctx, &dtomemory.MemoryBackfillReq{ScopeKey: ScopeKey(projectID), MaxSources: 1}); err == nil {
		t.Fatal("disabled memory must be rejected")
	}
}

// 回填等待开关恢复：关闭时 blocked，重新开启后由 unblockJobs 恢复并完成。
func TestBackfillBlockedThenUnblocked(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	conv := makeConversation(t, ctx, client, "conv-block", projectID, kaguyaconversation.MemoryModeInherit)
	makeTurn(t, ctx, client, conv, "等待恢复")
	resp, err := svc.StartBackfill(ctx, &dtomemory.MemoryBackfillReq{ScopeKey: ScopeKey(projectID), MaxSources: 10})
	if err != nil {
		t.Fatal(err)
	}
	setupPolicy(t, ctx, client, false, false)
	worker := NewWorker(svc)
	job := worker.claimBackfillJob(ctx)
	if job == nil {
		t.Fatal("missing job")
	}
	svc.runBackfill(ctx, job)
	blocked, err := client.KaguyaMemoryJob.Get(ctx, resp.JobID)
	if err != nil || blocked.Status != kaguyamemoryjob.StatusBlocked || blocked.ErrorCode != "config" {
		t.Fatalf("blocked=%+v err=%v", blocked, err)
	}
	setupPolicy(t, ctx, client, true, true)
	if !worker.unblockJobs(ctx) {
		t.Fatal("unblockJobs did not restore backfill")
	}
	runBackfillToCompletion(t, svc, 5)
	final, err := client.KaguyaMemoryJob.Get(ctx, resp.JobID)
	if err != nil || final.Status != kaguyamemoryjob.StatusSucceeded {
		t.Fatalf("final=%+v err=%v", final, err)
	}
	if count, err := client.KaguyaMemorySource.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("sources=%d err=%v", count, err)
	}
}
