package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryjob"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorypage"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
)

// compileFixture 建立“项目会话 completed 轮次已捕获”的编译测试环境。
type compileFixture struct {
	ctx      context.Context
	svc      *Service
	worker   *Worker
	client   *ent.Client
	conv     string
	scope    string
	sourceID string
}

// addTurn 追加一轮 completed 问答并捕获来源，返回新来源 ID（模拟下一批输入）。
func (f *compileFixture) addTurn(t *testing.T, user string, answers ...string) string {
	t.Helper()
	turnID := makeTurn(t, f.ctx, f.client, f.conv, user, answers...)
	captureTurn(t, f.ctx, f.client, f.conv, turnID)
	src, err := f.client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.TurnIDEQ(turnID)).Only(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.sourceID = src.ID
	return src.ID
}

func setupCompile(t *testing.T, caller *fakeCaller) *compileFixture {
	t.Helper()
	ctx, base, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	conv := makeConversation(t, ctx, client, "conv-compile", projectID, kaguyaconversation.MemoryModeInherit)
	turnID := makeTurn(t, ctx, client, conv, "这个项目继续用 SQLCipher，不引入第二个数据库。", "好的，Memory 继续复用 SQLCipher。")
	captureTurn(t, ctx, client, conv, turnID)
	src, err := client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.TurnIDEQ(turnID)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	svc := memoryServiceWith(base, caller)
	return &compileFixture{
		ctx: ctx, svc: svc, worker: NewWorker(svc), client: client, conv: conv,
		scope: "project:" + projectID, sourceID: src.ID,
	}
}

func (f *compileFixture) run(t *testing.T) *ent.KaguyaMemoryJob {
	t.Helper()
	backdateSources(t, f.ctx, f.client, 2*time.Minute)
	job, _ := f.worker.claimReadyBatch(f.ctx, "")
	if job == nil {
		t.Fatal("expected claimed job")
	}
	f.svc.runJob(f.ctx, job)
	return job
}

func testCandidate(sourceID string) dtomemory.Candidate {
	return dtomemory.Candidate{
		Key: "memory-storage", Kind: "decision",
		Title: "Memory 复用 SQLCipher", Statement: "项目要求 Memory 继续使用 SQLCipher，不增加第二个数据库。",
		Aliases: []string{"记忆存储"}, Basis: "user_statement",
		Evidence: []dtomemory.CandidateEvidence{{
			SourceID: sourceID, PartKey: "user", Quote: "继续用 SQLCipher，不引入第二个数据库",
		}},
	}
}

func testCreateChange(sourceID string) dtomemory.PagePatch {
	return dtomemory.PagePatch{
		Action: "create", CandidateKeys: []string{"memory-storage"},
		CanonicalKey: "memory-storage", Kind: "decision",
		Title: "Memory 复用 SQLCipher", Summary: "不增加第二个数据库",
		Body:    pageBody("Memory 与聊天数据共享 SQLCipher"),
		Aliases: []string{"记忆存储"},
		Claims: []dtomemory.ClaimPatch{{
			Key: "storage", Statement: "Memory 继续使用 SQLCipher", Basis: "user_statement",
			Evidence: []dtomemory.ClaimEvidence{{
				SourceID: sourceID, PartKey: "user", Quote: "继续用 SQLCipher", Relation: "support",
			}},
		}},
		RelatedIDs: []string{},
		Reason:     "用户明确决定",
	}
}

// 完整编译闭环：提炼 → 合并 → 确定性校验 → 单事务发布页面、修订、证据与搜索投影。
func TestCompileCreatesPageWithEvidence(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
		func(int) (CallResult, error) { return okResult(planJSON(testCreateChange(f.sourceID))), nil },
	}
	f.run(t)

	page, err := f.client.KaguyaMemoryPage.Query().Only(f.ctx)
	if err != nil || page.ScopeKey != f.scope || page.Status != kaguyamemorypage.StatusActive || page.Version != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	revisions, err := f.client.KaguyaMemoryRevision.Query().All(f.ctx)
	if err != nil || len(revisions) != 1 || revisions[0].Actor != "task_model" || revisions[0].Version != 1 {
		t.Fatalf("revisions=%+v err=%v", revisions, err)
	}
	evidence, err := f.client.KaguyaMemoryEvidence.Query().All(f.ctx)
	if err != nil || len(evidence) != 1 || evidence[0].SourceID != f.sourceID || evidence[0].Basis != "user_statement" {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	if count, err := f.client.KaguyaMemorySearchDoc.Query().Count(f.ctx); err != nil || count != 1 {
		t.Fatalf("search doc: count=%d err=%v", count, err)
	}
	src, err := f.client.KaguyaMemorySource.Get(f.ctx, f.sourceID)
	if err != nil || src.State != kaguyamemorysource.StateProcessed {
		t.Fatalf("source=%+v err=%v", src, err)
	}
	job, err := f.client.KaguyaMemoryJob.Query().Only(f.ctx)
	if err != nil || job.Status != kaguyamemoryjob.StatusSucceeded || job.ResultJSON == "" {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	// 用量记入 Attempt：成功与未知分开，聊天口径不受影响。
	attempts, err := f.client.KaguyaMemoryAttempt.Query().All(f.ctx)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
	for _, attempt := range attempts {
		if !attempt.UsageKnown || attempt.ResultCode != "ok" {
			t.Fatalf("attempt=%+v", attempt)
		}
	}
}

// 合法空输出标记 noop：不调用第二次模型，来源不伪装成已处理。
func TestCompileEmptyOutputMarksNoop(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON()), nil },
	}
	f.run(t)
	if n := int(caller.mu.Load()); n != 1 {
		t.Fatalf("noop must skip planning, calls=%d", n)
	}
	src, err := f.client.KaguyaMemorySource.Get(f.ctx, f.sourceID)
	if err != nil || src.State != kaguyamemorysource.StateNoop {
		t.Fatalf("source=%+v err=%v", src, err)
	}
	job, err := f.client.KaguyaMemoryJob.Query().Only(f.ctx)
	if err != nil || job.Status != kaguyamemoryjob.StatusSucceeded {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if count, err := f.client.KaguyaMemoryPage.Query().Count(f.ctx); err != nil || count != 0 {
		t.Fatalf("pages=%d err=%v", count, err)
	}
}

// 已有页面按 base_version 乐观并发更新，产生新修订。
func TestCompileUpdatesExistingPage(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	existing, err := f.svc.CreatePage(f.ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: f.scope, Kind: "decision", CanonicalKey: "memory-storage",
		Title: "Memory 存储", Body: pageBody("旧内容"),
	})
	if err != nil {
		t.Fatal(err)
	}
	change := testCreateChange(f.sourceID)
	change.Action = "update"
	change.PageID = existing.ID
	change.BaseVersion = existing.Version
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
		func(int) (CallResult, error) { return okResult(planJSON(change)), nil },
	}
	f.run(t)
	page, err := f.client.KaguyaMemoryPage.Get(f.ctx, existing.ID)
	if err != nil || page.Version != 2 || !strings.Contains(page.Body, "Memory 与聊天数据共享 SQLCipher") {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	revisions, err := f.client.KaguyaMemoryRevision.Query().All(f.ctx)
	if err != nil || len(revisions) != 2 {
		t.Fatalf("revisions=%+v err=%v", revisions, err)
	}
}

// 强证据冲突：原页面标 conflicted 移出自动召回，提案进待审；
// 用户批准后以当前版本发布，拒绝则只记录处理结果。
func TestCompileConflictReviewAndResolve(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	existing, err := f.svc.CreatePage(f.ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: f.scope, Kind: "decision", CanonicalKey: "memory-storage",
		Title: "Memory 存储", Body: pageBody("旧内容"),
	})
	if err != nil {
		t.Fatal(err)
	}
	change := testCreateChange(f.sourceID)
	change.Action = "conflict"
	change.PageID = existing.ID
	change.BaseVersion = existing.Version
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
		func(int) (CallResult, error) { return okResult(planJSON(change)), nil },
	}
	job := f.run(t)
	page, err := f.client.KaguyaMemoryPage.Get(f.ctx, existing.ID)
	if err != nil || page.Status != kaguyamemorypage.StatusConflicted || page.Version != 2 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	// conflicted 页面移出自动召回投影。
	if count, err := f.client.KaguyaMemorySearchDoc.Query().Count(f.ctx); err != nil || count != 0 {
		t.Fatalf("conflicted page indexed: count=%d err=%v", count, err)
	}
	refreshed, err := f.client.KaguyaMemoryJob.Get(f.ctx, job.ID)
	if err != nil || refreshed.Status != kaguyamemoryjob.StatusNeedsReview || refreshed.ResultJSON == "" {
		t.Fatalf("job=%+v err=%v", refreshed, err)
	}
	if _, err := f.svc.ApproveReview(f.ctx, refreshed); err != nil {
		t.Fatal(err)
	}
	page, err = f.client.KaguyaMemoryPage.Get(f.ctx, existing.ID)
	if err != nil || page.Version != 3 || page.Status != kaguyamemorypage.StatusActive {
		t.Fatalf("approved page=%+v err=%v", page, err)
	}

	// 拒绝路径：记录处理结果，不产生新修订。下一批输入来自新一轮问答。
	f.addTurn(t, "再确认一次：继续用 SQLCipher，不引入第二个数据库。", "依旧复用 SQLCipher。")
	change2 := testCreateChange(f.sourceID)
	change2.Action = "conflict"
	change2.PageID = page.ID
	change2.BaseVersion = page.Version
	caller.steps = append(caller.steps,
		func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
		func(int) (CallResult, error) { return okResult(planJSON(change2)), nil },
	)
	f.run(t)
	reviewJob, err := f.client.KaguyaMemoryJob.Query().
		Where(kaguyamemoryjob.StatusEQ(kaguyamemoryjob.StatusNeedsReview)).Only(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RejectReview(f.ctx, reviewJob); err != nil {
		t.Fatal(err)
	}
	rejected, err := f.client.KaguyaMemoryJob.Get(f.ctx, reviewJob.ID)
	if err != nil || rejected.Status != kaguyamemoryjob.StatusCanceled || rejected.ErrorCode != "review_rejected" {
		t.Fatalf("rejected job=%+v err=%v", rejected, err)
	}
}

// 人工锁定页面的自动更新只进待审；批准后由用户动作发布。
func TestCompileLockedPageUpdateGoesToReview(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	locked := true
	existing, err := f.svc.CreatePage(f.ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: f.scope, Kind: "decision", CanonicalKey: "memory-storage",
		Title: "Memory 存储", Body: pageBody("用户锁定内容"), UserLocked: &locked,
	})
	if err != nil {
		t.Fatal(err)
	}
	change := testCreateChange(f.sourceID)
	change.Action = "update"
	change.PageID = existing.ID
	change.BaseVersion = existing.Version
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
		func(int) (CallResult, error) { return okResult(planJSON(change)), nil },
	}
	f.run(t)
	page, err := f.client.KaguyaMemoryPage.Get(f.ctx, existing.ID)
	if err != nil || page.Version != 1 || !strings.Contains(page.Body, "用户锁定内容") {
		t.Fatalf("locked page overwritten: %+v err=%v", page, err)
	}
	job, err := f.client.KaguyaMemoryJob.Query().
		Where(kaguyamemoryjob.StatusEQ(kaguyamemoryjob.StatusNeedsReview)).Only(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ApproveReview(f.ctx, job); err != nil {
		t.Fatal(err)
	}
	page, err = f.client.KaguyaMemoryPage.Get(f.ctx, existing.ID)
	if err != nil || page.Version != 2 || page.UserLocked != true {
		t.Fatalf("approved page=%+v err=%v", page, err)
	}
}

// tombstone 阻止自动复活；被放弃的变更不影响其他发布。
func TestCompileTombstoneCreateDropped(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	forgotten, err := f.svc.CreatePage(f.ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: f.scope, Kind: "decision", CanonicalKey: "memory-storage",
		Title: "Memory 存储", Body: pageBody("旧内容"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeletePage(f.ctx, forgotten.ID, "forget"); err != nil {
		t.Fatal(err)
	}
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
		func(int) (CallResult, error) { return okResult(planJSON(testCreateChange(f.sourceID))), nil },
	}
	f.run(t)
	page, err := f.client.KaguyaMemoryPage.Get(f.ctx, forgotten.ID)
	if err != nil || page.Status != kaguyamemorypage.StatusDeleted || page.Body != "" {
		t.Fatalf("tombstone revived: %+v err=%v", page, err)
	}
	if count, err := f.client.KaguyaMemoryPage.Query().Count(f.ctx); err != nil || count != 1 {
		t.Fatalf("pages=%d err=%v", count, err)
	}
}

// 校验失败不能当作 noop：作业 failed、来源 failed 可人工重试。
func TestCompileInvalidPlanFailsJob(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	bad := testCreateChange(f.sourceID)
	bad.Claims[0].Evidence[0].Quote = "凭空引用"
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
		func(int) (CallResult, error) { return okResult(planJSON(bad)), nil },
		func(int) (CallResult, error) { return okResult(planJSON(bad)), nil },
	}
	f.run(t)
	job, err := f.client.KaguyaMemoryJob.Query().Only(f.ctx)
	if err != nil || job.Status != kaguyamemoryjob.StatusFailed || job.ErrorCode != "invalid_output" {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	src, err := f.client.KaguyaMemorySource.Get(f.ctx, f.sourceID)
	if err != nil || src.State != kaguyamemorysource.StateFailed {
		t.Fatalf("source=%+v err=%v", src, err)
	}
}

// 格式错误最多一次有界修复；修复成功照常发布，修复仍失败则 failed。
func TestCompileRepairsInvalidJSONOnce(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult("{not json"), nil },
		func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
		func(int) (CallResult, error) { return okResult(planJSON(testCreateChange(f.sourceID))), nil },
	}
	f.run(t)
	if n := int(caller.mu.Load()); n != 3 {
		t.Fatalf("calls=%d", n)
	}
	job, err := f.client.KaguyaMemoryJob.Query().Only(f.ctx)
	if err != nil || job.Status != kaguyamemoryjob.StatusSucceeded {
		t.Fatalf("job=%+v err=%v", job, err)
	}

	// 修复仍然非法：failed，不再继续修复。
	caller2 := &fakeCaller{}
	f2 := setupCompile(t, caller2)
	caller2.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult("{not json"), nil },
		func(int) (CallResult, error) { return okResult("still not json"), nil },
	}
	f2.run(t)
	job2, err := f2.client.KaguyaMemoryJob.Query().Only(f2.ctx)
	if err != nil || job2.Status != kaguyamemoryjob.StatusFailed || job2.ErrorCode != "invalid_output" {
		t.Fatalf("job=%+v err=%v", job2, err)
	}
	if n := int(caller2.mu.Load()); n != 2 {
		t.Fatalf("repair calls=%d", n)
	}
}

// 旧租约结果拒绝：token 变更后本 attempt 不能提交。
func TestCompileStaleLeaseRejected(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
		func(int) (CallResult, error) { return okResult(planJSON(testCreateChange(f.sourceID))), nil },
	}
	backdateSources(t, f.ctx, f.client, 2*time.Minute)
	job, _ := f.worker.claimReadyBatch(f.ctx, "")
	if job == nil {
		t.Fatal("expected claimed job")
	}
	// 模拟新 worker 接管：租约 token 换成新值。
	if err := f.client.KaguyaMemoryJob.UpdateOneID(job.ID).SetLeaseToken("taken-over").Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.svc.runJob(f.ctx, job)
	if count, err := f.client.KaguyaMemoryPage.Query().Count(f.ctx); err != nil || count != 0 {
		t.Fatalf("stale lease published: count=%d err=%v", count, err)
	}
}

// 策略版本失效：在途结果保守放弃，来源回到待处理重新校验。
func TestCompilePolicyEpochAbort(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
		func(int) (CallResult, error) { return okResult(planJSON(testCreateChange(f.sourceID))), nil },
	}
	backdateSources(t, f.ctx, f.client, 2*time.Minute)
	job, _ := f.worker.claimReadyBatch(f.ctx, "")
	if job == nil {
		t.Fatal("expected claimed job")
	}
	if err := BumpPolicyEpoch(f.ctx, f.client); err != nil {
		t.Fatal(err)
	}
	f.svc.runJob(f.ctx, job)
	if count, err := f.client.KaguyaMemoryPage.Query().Count(f.ctx); err != nil || count != 0 {
		t.Fatalf("epoch change published: count=%d err=%v", count, err)
	}
	refreshed, err := f.client.KaguyaMemoryJob.Get(f.ctx, job.ID)
	if err != nil || refreshed.Status != kaguyamemoryjob.StatusCanceled || refreshed.ErrorCode != "policy_changed" {
		t.Fatalf("job=%+v err=%v", refreshed, err)
	}
	src, err := f.client.KaguyaMemorySource.Get(f.ctx, f.sourceID)
	if err != nil || src.State != kaguyamemorysource.StatePending {
		t.Fatalf("source=%+v err=%v", src, err)
	}
}

// 错误分类：认证失败 blocked 且不消耗执行尝试；瞬时错误退避重试；上限后 failed。
func TestCompileFailureClassification(t *testing.T) {
	t.Run("auth blocked", func(t *testing.T) {
		caller := &fakeCaller{}
		f := setupCompile(t, caller)
		caller.steps = []func(int) (CallResult, error){
			func(int) (CallResult, error) {
				return CallResult{}, &fantasy.ProviderError{StatusCode: 401, Message: "unauthorized"}
			},
		}
		f.run(t)
		job, err := f.client.KaguyaMemoryJob.Query().Only(f.ctx)
		if err != nil || job.Status != kaguyamemoryjob.StatusBlocked || job.ErrorCode != "auth" || job.Attempt != 0 {
			t.Fatalf("job=%+v err=%v", job, err)
		}
	})
	t.Run("transient retry then fail", func(t *testing.T) {
		caller := &fakeCaller{}
		f := setupCompile(t, caller)
		transient := func(int) (CallResult, error) {
			return CallResult{}, &fantasy.ProviderError{StatusCode: 503, Message: "upstream"}
		}
		caller.steps = []func(int) (CallResult, error){transient}
		f.run(t)
		job, err := f.client.KaguyaMemoryJob.Query().Only(f.ctx)
		if err != nil || job.Status != kaguyamemoryjob.StatusRetryWait || job.NextAttemptAt == nil {
			t.Fatalf("job=%+v err=%v", job, err)
		}
		// 到期重试直到上限 failed。
		for i := 0; i < maxJobAttempts; i++ {
			if err := f.client.KaguyaMemoryJob.UpdateOneID(job.ID).
				SetNextAttemptAt(time.Now().Add(-time.Second)).Exec(f.ctx); err != nil {
				t.Fatal(err)
			}
			due := f.worker.claimDueJob(f.ctx)
			if due == nil {
				break
			}
			caller.steps = append(caller.steps, transient)
			f.svc.runJob(f.ctx, due)
		}
		finalJob, err := f.client.KaguyaMemoryJob.Query().Only(f.ctx)
		if err != nil || finalJob.Status != kaguyamemoryjob.StatusFailed {
			t.Fatalf("job=%+v err=%v", finalJob, err)
		}
		src, err := f.client.KaguyaMemorySource.Get(f.ctx, f.sourceID)
		if err != nil || src.State != kaguyamemorysource.StateFailed {
			t.Fatalf("source=%+v err=%v", src, err)
		}
		// 失败重试的调用同样计费：usage_known=false 不能按 0 计入确认消耗。
		attempts, err := f.client.KaguyaMemoryAttempt.Query().All(f.ctx)
		if err != nil || len(attempts) < 2 {
			t.Fatalf("attempts=%+v err=%v", attempts, err)
		}
		for _, attempt := range attempts {
			if attempt.UsageKnown {
				t.Fatalf("error attempt must record unknown usage: %+v", attempt)
			}
		}
	})
}

// 人工重试把 failed 作业与来源恢复为可执行状态。
func TestRetryJobRestoresSources(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) {
			return CallResult{}, errors.New("broken")
		},
	}
	// 直接构造终态失败。
	backdateSources(t, f.ctx, f.client, 2*time.Minute)
	job, _ := f.worker.claimReadyBatch(f.ctx, "")
	f.svc.runJob(f.ctx, job)
	job, err := f.client.KaguyaMemoryJob.Get(f.ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.failJob(f.ctx, job, "manual", "forced"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RetryJob(f.ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	refreshed, err := f.client.KaguyaMemoryJob.Get(f.ctx, job.ID)
	if err != nil || refreshed.Status != kaguyamemoryjob.StatusRetryWait || refreshed.Attempt != 0 {
		t.Fatalf("job=%+v err=%v", refreshed, err)
	}
	src, err := f.client.KaguyaMemorySource.Get(f.ctx, f.sourceID)
	if err != nil || src.State != kaguyamemorysource.StateClaimed {
		t.Fatalf("source=%+v err=%v", src, err)
	}
}
