package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"
	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatblock"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryjob"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryrevision"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
)

// setupCompileWithTarget 用指定任务模型窗口建立编译环境，用于验证拆批预算。
func setupCompileWithTarget(t *testing.T, caller *fakeCaller, target *servicesystem.TaskModel) *compileFixture {
	t.Helper()
	ctx, base, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	projectID := makeProject(t, ctx, client, "p1")
	conv := makeConversation(t, ctx, client, "conv-compile", projectID, kaguyaconversation.MemoryModeInherit)
	turnID := makeTurn(t, ctx, client, conv, "请记住四个主题的约束。", "好的。")
	captureTurn(t, ctx, client, conv, turnID)
	src, err := client.KaguyaMemorySource.Query().Where(kaguyamemorysource.TurnIDEQ(turnID)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	svc := memoryServiceWithTarget(base, caller, target)
	return &compileFixture{
		ctx: ctx, svc: svc, worker: NewWorker(svc), client: client, conv: conv,
		scope: "project:" + projectID, sourceID: src.ID,
	}
}

// memoryServiceWithTarget 构造使用指定任务模型快照与 fake caller 的服务。
func memoryServiceWithTarget(svc *Service, caller *fakeCaller, target *servicesystem.TaskModel) *Service {
	return svc.WithCaller(caller).WithTaskModelResolver(
		func(context.Context, *ent.Client, string) (*servicesystem.TaskModel, error) {
			return target, nil
		})
}

// batchingTopic 是一组“候选 + 已有大页面”的测试主题。
type batchingTopic struct {
	key    string
	title  string
	page   *MemoryPageDetail
	change dtomemory.PagePatch
}

// setupBatchingTopics 创建 4 个大页面与对应候选，单页接近正文上限，
// 强制阶段 C 输入超过小窗口模型的单次预算。
func setupBatchingTopics(t *testing.T, f *compileFixture) []batchingTopic {
	t.Helper()
	topics := make([]batchingTopic, 0, 4)
	for i := 0; i < 4; i++ {
		key := fmt.Sprintf("topic-%d", i)
		title := fmt.Sprintf("主题%d", i)
		page, err := f.svc.CreatePage(f.ctx, &dtomemory.MemoryPageSaveReq{
			ScopeKey: f.scope, Kind: "fact", CanonicalKey: key, Title: title,
			Summary: "大页面摘要", Body: strings.Repeat("a", 7000),
		})
		if err != nil {
			t.Fatal(err)
		}
		topics = append(topics, batchingTopic{
			key: key, title: title, page: page,
			change: dtomemory.PagePatch{
				Action: "update", CandidateKeys: []string{key},
				PageID: page.ID, BaseVersion: page.Version,
				Kind: "fact", Title: title, Summary: "更新摘要",
				Body:    strings.Repeat("b", 7000),
				Aliases: []string{},
				Claims: []dtomemory.ClaimPatch{{
					Key: key + "-claim", Statement: "更新后的主张", Basis: "user_statement",
					Evidence: []dtomemory.ClaimEvidence{{
						SourceID: f.sourceID, PartKey: "user", Quote: "四个主题", Relation: "support",
					}},
				}},
				RelatedIDs: []string{},
				Reason:     "测试拆批",
			},
		})
	}
	return topics
}

func batchingCandidates(f *compileFixture, topics []batchingTopic) []dtomemory.Candidate {
	out := make([]dtomemory.Candidate, 0, len(topics))
	for _, topic := range topics {
		out = append(out, dtomemory.Candidate{
			Key: topic.key, Kind: "fact", Title: topic.title,
			Statement: "需要更新的主题约束", Aliases: []string{},
			Basis: "user_statement",
			Evidence: []dtomemory.CandidateEvidence{{
				SourceID: f.sourceID, PartKey: "user", Quote: "四个主题",
			}},
		})
	}
	return out
}

// planStepForTopics 按提示词中出现的候选 key 返回对应更新，模拟每批一次的阶段 C。
func planStepForTopics(caller *fakeCaller, topics []batchingTopic) func(int) (CallResult, error) {
	return func(index int) (CallResult, error) {
		payload := caller.prompts[index]
		for _, topic := range topics {
			if strings.Contains(payload, `"`+topic.key+`"`) {
				return okResult(planJSON(topic.change)), nil
			}
		}
		t := caller.prompts[index]
		_ = t
		return CallResult{}, errors.New("plan payload matches no candidate")
	}
}

// 捕获与业务事实同事务：事务回滚后不留下孤立来源。
func TestCaptureRollsBackWithBusinessData(t *testing.T) {
	ctx, _, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	conv := makeConversation(t, ctx, client, "conv-rollback", "", kaguyaconversation.MemoryModeInherit)
	turnID := makeTurn(t, ctx, client, conv, "同事务来源")
	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	txC := tx.Client()
	if err := CaptureCompletedTx(ctx, txC, CaptureInput{ConversationID: conv, TurnID: turnID}); err != nil {
		t.Fatal(err)
	}
	if count, err := txC.KaguyaMemorySource.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("in-tx sources=%d err=%v", count, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if count, err := client.KaguyaMemorySource.Query().Count(ctx); err != nil || count != 0 {
		t.Fatalf("rolled-back sources=%d err=%v", count, err)
	}
}

// 单个候选页集合无法连同来源一起放进模型预算时，按优先级收缩旧页面而不是阻塞；
// 候选本身仍得到一次完整处理。
func TestSplitPlanBatchesShrinksOversizedCandidatePages(t *testing.T) {
	scope := "project:p1"
	input := []byte(`[{"source_id":"s1","scope_key":"project:p1","parts_total":1,"segments":[{"part_key":"user","origin":"user_statement","text":"x"}]}]`)
	found := &mergeCandidates{pages: map[string]*ent.KaguyaMemoryPage{}, pagesByCandidate: map[string][]string{}, data: map[string]candidatePageData{}}
	pageID := "page-large"
	found.pages[pageID] = &ent.KaguyaMemoryPage{ID: pageID, ScopeKey: scope, Version: 1, Title: "大页面", Body: strings.Repeat("a", 7000)}
	found.pagesByCandidate["topic"] = []string{pageID}
	candidates := []dtomemory.Candidate{{Key: "topic", Title: "大页面", Statement: "陈述", Basis: "user_statement"}}
	batches, err := splitPlanBatches(scope, input, candidates, found, 1500)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 || len(batches[0].candidates) != 1 || len(batches[0].pages) != 0 {
		t.Fatalf("batches=%+v", batches)
	}
}

// 超预算合并自动拆批：小窗口模型下 4 个大页面分成多次阶段 C 调用，
// 全部更新在同一发布事务中提交，没有阻塞、遗漏或重复。
func TestPlanBatchesSplitByModelBudget(t *testing.T) {
	caller := &fakeCaller{}
	target := fakeTaskModel()
	target.TokenContextWindow = 6000
	f := setupCompileWithTarget(t, caller, target)
	topics := setupBatchingTopics(t, f)
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(batchingCandidates(f, topics)...)), nil },
	}
	for range topics {
		caller.steps = append(caller.steps, planStepForTopics(caller, topics))
	}
	f.run(t)

	planCalls := int(caller.mu.Load()) - 1
	if planCalls < 2 {
		t.Fatalf("expected split plan calls, got %d", planCalls)
	}
	for _, topic := range topics {
		page, err := f.client.KaguyaMemoryPage.Get(f.ctx, topic.page.ID)
		if err != nil || page.Version != 2 || !strings.HasPrefix(page.Body, "b") {
			t.Fatalf("topic %s page=%+v err=%v", topic.key, page, err)
		}
	}
	revisions, err := f.client.KaguyaMemoryRevision.Query().Count(f.ctx)
	if err != nil || revisions != 8 { // 4 个手工页面 v1 + 4 个编译更新 v2
		t.Fatalf("revisions=%d err=%v", revisions, err)
	}
	job, err := f.client.KaguyaMemoryJob.Query().Only(f.ctx)
	if err != nil || job.Status != kaguyamemoryjob.StatusSucceeded {
		t.Fatalf("job=%+v err=%v", job, err)
	}
}

// 拆批结果确定性：同一冻结输入两次拆批得到完全相同的候选与页面分组。
func TestSplitPlanBatchesDeterministic(t *testing.T) {
	scope := "project:p1"
	input := []byte(`[{"source_id":"s1","scope_key":"project:p1","parts_total":1,"segments":[{"part_key":"user","origin":"user_statement","text":"x"}]}]`)
	found := &mergeCandidates{pages: map[string]*ent.KaguyaMemoryPage{}, pagesByCandidate: map[string][]string{}, data: map[string]candidatePageData{}}
	candidates := make([]dtomemory.Candidate, 0, 4)
	for i := 0; i < 4; i++ {
		key := fmt.Sprintf("topic-%d", i)
		pageID := fmt.Sprintf("page-%d", i)
		found.pages[pageID] = &ent.KaguyaMemoryPage{ID: pageID, ScopeKey: scope, Version: 1, Title: key, Body: strings.Repeat("a", 7000)}
		found.pagesByCandidate[key] = []string{pageID}
		candidates = append(candidates, dtomemory.Candidate{Key: key, Title: key, Statement: "s", Basis: "user_statement"})
	}
	describe := func() string {
		batches, err := splitPlanBatches(scope, input, candidates, found, 12000)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, batch := range batches {
			for _, candidate := range batch.candidates {
				fmt.Fprintf(&b, "%s|", candidate.Key)
			}
			ids := make([]string, 0, len(batch.pages))
			for id := range batch.pages {
				ids = append(ids, id)
			}
			b.WriteString(strings.Join(ids, ","))
			b.WriteString(";")
		}
		return b.String()
	}
	first := describe()
	for i := 0; i < 3; i++ {
		if again := describe(); again != first {
			t.Fatalf("batch split is not deterministic:\n%s\n%s", first, again)
		}
	}
}

// 单个来源超过单次预算时按 segment 切分并推进游标，剩余部分仍待处理。
func TestSingleSourceOverBudgetSplitsWithCursor(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	// 覆盖一个超长轮次：约 30 个 6000 字节分段。
	long := strings.Repeat("长内容分段。", 5000)
	turnID := makeTurn(t, f.ctx, f.client, f.conv, long)
	captureTurn(t, f.ctx, f.client, f.conv, turnID)
	src, err := f.client.KaguyaMemorySource.Query().Where(kaguyamemorysource.TurnIDEQ(turnID)).Only(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 丢弃初始 fixture 来源，只验证长来源的分段推进。
	if _, err := f.client.KaguyaMemorySource.Delete().Where(kaguyamemorysource.IDEQ(f.sourceID)).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	backdateSources(t, f.ctx, f.client, 2*time.Minute)
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON()), nil },
	}
	job, _ := f.worker.claimReadyBatch(f.ctx, "")
	if job == nil {
		t.Fatal("missing job")
	}
	if len(job.InputSourceIds) != 1 || job.InputSourceIds[0] != src.ID {
		t.Fatalf("job sources=%v want %s", job.InputSourceIds, src.ID)
	}
	f.svc.runJob(f.ctx, job)
	first, err := f.client.KaguyaMemorySource.Get(f.ctx, src.ID)
	if err != nil || first.CursorPart == 0 || first.State != kaguyamemorysource.StatePending {
		t.Fatalf("first batch source=%+v err=%v", first, err)
	}
	// 继续处理直到覆盖全部分段；每次都按 segment 推进，不丢尾部。
	for round := 0; round < 40; round++ {
		current, err := f.client.KaguyaMemorySource.Get(f.ctx, src.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State != kaguyamemorysource.StatePending {
			break
		}
		caller.steps = append(caller.steps, func(int) (CallResult, error) { return okResult(extractJSON()), nil })
		if err := f.client.KaguyaMemorySource.UpdateOneID(src.ID).
			SetCapturedAt(time.Now().Add(-2 * time.Minute)).Exec(f.ctx); err != nil {
			t.Fatal(err)
		}
		next, _ := f.worker.claimReadyBatch(f.ctx, "")
		if next == nil {
			t.Fatalf("round %d: missing job", round)
		}
		f.svc.runJob(f.ctx, next)
	}
	final, err := f.client.KaguyaMemorySource.Get(f.ctx, src.ID)
	if err != nil || final.State != kaguyamemorysource.StateNoop || final.CursorPart == 0 {
		t.Fatalf("final source=%+v err=%v", final, err)
	}
	segments, err := LoadSourceSegments(f.ctx, f.client, final)
	if err != nil || final.CursorPart != len(segments) {
		t.Fatalf("cursor=%d segments=%d err=%v", final.CursorPart, len(segments), err)
	}
}

// 部分批次失败时整体回滚，重试后只发布一次，不产生重复修订。
func TestPlanBatchPartialFailureRetriesCleanly(t *testing.T) {
	caller := &fakeCaller{}
	target := fakeTaskModel()
	target.TokenContextWindow = 6000
	f := setupCompileWithTarget(t, caller, target)
	topics := setupBatchingTopics(t, f)
	transient := func(int) (CallResult, error) {
		return CallResult{}, &fantasy.ProviderError{StatusCode: 503, Message: "upstream"}
	}
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(batchingCandidates(f, topics)...)), nil },
		planStepForTopics(caller, topics),
		transient,
	}
	backdateSources(t, f.ctx, f.client, 2*time.Minute)
	job, _ := f.worker.claimReadyBatch(f.ctx, "")
	if job == nil {
		t.Fatal("missing job")
	}
	f.svc.runJob(f.ctx, job)
	failed, err := f.client.KaguyaMemoryJob.Get(f.ctx, job.ID)
	if err != nil || failed.Status != kaguyamemoryjob.StatusRetryWait {
		t.Fatalf("job=%+v err=%v", failed, err)
	}
	// 失败批次未发布任何页面变更。
	for _, topic := range topics {
		page, err := f.client.KaguyaMemoryPage.Get(f.ctx, topic.page.ID)
		if err != nil || page.Version != 1 {
			t.Fatalf("partial publish: topic=%s page=%+v err=%v", topic.key, page, err)
		}
	}
	// 到期重试：重新执行全部批次，只发布一次。
	if err := f.client.KaguyaMemoryJob.UpdateOneID(job.ID).
		SetNextAttemptAt(time.Now().Add(-time.Second)).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	retry := f.worker.claimDueJob(f.ctx)
	if retry == nil {
		t.Fatal("missing retry job")
	}
	caller.steps = append(caller.steps,
		func(int) (CallResult, error) { return okResult(extractJSON(batchingCandidates(f, topics)...)), nil })
	for range topics {
		caller.steps = append(caller.steps, planStepForTopics(caller, topics))
	}
	f.svc.runJob(f.ctx, retry)
	for _, topic := range topics {
		page, err := f.client.KaguyaMemoryPage.Get(f.ctx, topic.page.ID)
		if err != nil || page.Version != 2 {
			t.Fatalf("retry topic=%s page=%+v err=%v", topic.key, page, err)
		}
	}
	// 每页恰好一条编译修订，重试没有重复发布。
	for _, topic := range topics {
		count, err := f.client.KaguyaMemoryRevision.Query().
			Where(kaguyamemoryrevision.PageIDEQ(topic.page.ID), kaguyamemoryrevision.ActorEQ(kaguyamemoryrevision.ActorTaskModel)).
			Count(f.ctx)
		if err != nil || count != 1 {
			t.Fatalf("topic=%s task revisions=%d err=%v", topic.key, count, err)
		}
	}
}

// 并发领取同一批来源只允许一个作业成功，不能把来源挂到两个作业。
func TestConcurrentClaimDoesNotDoubleClaim(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	backdateSources(t, f.ctx, f.client, 2*time.Minute)
	group, err := f.client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.StateEQ(kaguyamemorysource.StatePending)).All(f.ctx)
	if err != nil || len(group) == 0 {
		t.Fatalf("group=%v err=%v", group, err)
	}
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, results[index] = f.svc.createJobForSources(f.ctx, group, maxBatchSourceChars)
		}(i)
	}
	wg.Wait()
	success, stale := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrStaleLease):
			stale++
		default:
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatalf("success=%d stale=%d results=%v", success, stale, results)
	}
	if count, err := f.client.KaguyaMemoryJob.Query().Count(f.ctx); err != nil || count != 1 {
		t.Fatalf("jobs=%d err=%v", count, err)
	}
	for _, src := range group {
		row, err := f.client.KaguyaMemorySource.Get(f.ctx, src.ID)
		if err != nil || row.State != kaguyamemorysource.StateClaimed {
			t.Fatalf("source=%+v err=%v", row, err)
		}
	}
}

// 轻量 Outbox：没有可见分段的来源被标记 noop，不生成空作业也不永久 pending。
func TestEmptyProjectionSourceMarkedNoop(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	emptyTurn := makeTurn(t, f.ctx, f.client, f.conv, "")
	captureTurn(t, f.ctx, f.client, f.conv, emptyTurn)
	backdateSources(t, f.ctx, f.client, 2*time.Minute)
	job, _ := f.worker.claimReadyBatch(f.ctx, "")
	if job == nil {
		t.Fatal("expected a job for the non-empty source")
	}
	emptySrc, err := f.client.KaguyaMemorySource.Query().Where(kaguyamemorysource.TurnIDEQ(emptyTurn)).Only(f.ctx)
	if err != nil || emptySrc.State != kaguyamemorysource.StateNoop {
		t.Fatalf("empty source=%+v err=%v", emptySrc, err)
	}
	if count, err := f.client.KaguyaMemoryJob.Query().Count(f.ctx); err != nil || count != 1 {
		t.Fatalf("jobs=%d err=%v", count, err)
	}
}

// 捕获事务只记录事实：投影缺失时 Worker 仍能从轮次重建，不需要捕获时预读。
func TestCaptureSurvivesBlockRemovalAndDerivesLater(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	setupPolicy(t, ctx, client, true, true)
	conv := makeConversation(t, ctx, client, "conv-outbox", "", kaguyaconversation.MemoryModeInherit)
	turnID := makeTurn(t, ctx, client, conv, "保留的用户陈述", "助手回答")
	captureTurn(t, ctx, client, conv, turnID)
	// 删除展示块：捕获事务当时不依赖它们，投影稍后从用户内容重建。
	if _, err := client.KaguyaChatBlock.Delete().
		Where(kaguyachatblock.TurnIDEQ(turnID)).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	backdateSources(t, ctx, client, 2*time.Minute)
	job, _ := NewWorker(svc).claimReadyBatch(ctx, "")
	if job == nil {
		t.Fatal("missing job")
	}
	projection, err := svc.loadJobInput(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(projection), "保留的用户陈述") {
		t.Fatalf("projection=%s", projection)
	}
}
