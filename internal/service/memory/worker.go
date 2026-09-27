package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"entgo.io/ent/dialect/sql"
	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryattempt"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryevidence"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryjob"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorylink"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorypage"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryrevision"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
	"go.uber.org/zap"
)

// 防抖与批量参数（docs/memory-design.md 6.4），待评测的起始参数：
// 同一会话安静 60 秒后处理；最早来源等待达到 10 分钟即使持续活跃也处理一个批次；
// 累积 6 个待处理来源或用户点击“立即整理”可提前处理。
const (
	batchQuietWindow    = 60 * time.Second
	batchMaxWait        = 10 * time.Minute
	batchReadyCount     = 6
	maxBatchSourceChars = 48000
	// maxPlanInputBytes 是模型窗口未知时使用的单次输入预算，不是页面存储上限。
	// 超过预算自动拆批，而不是阻塞；单个候选页仍完整提供。
	maxPlanInputBytes = 128 << 10
	// planInputReserve 为系统提示与 JSON 包装预留，minPageReserve 预留合并输入空间。
	planInputReserve = 4 << 10
	minPageReserve   = 10 << 10
	minSourceBatch   = 4 << 10
	maxJobAttempts   = 3
	leaseDuration    = 10 * time.Minute
	// 日预算：达到后 blocked 而不是无限跑。
	memoryDailyCalls  = 400
	memoryDailyTokens = 2_000_000
	baseRetryDelay    = 30 * time.Second
)

// modelInputBudget 按任务模型窗口给单次调用输入一个保守字节上限；
// 窗口未知时使用固定上限。byte/4 估算不是 tokenizer，中文场景保留余量。
func modelInputBudget(target *servicesystem.TaskModel, reserve int) int {
	if target == nil || target.TokenContextWindow <= 0 {
		return maxPlanInputBytes
	}
	// 窗口的 3/4 可用于输入；window token × 4 byte/token 后即 window×3 字节。
	limit := target.TokenContextWindow*3 - reserve
	return max(limit, 1)
}

// sourceBudgetForModel 把来源批次收缩到模型可接受的规模；来源超预算时按
// segment 切分并记录覆盖范围，剩余部分仍待处理。
func sourceBudgetForModel(target *servicesystem.TaskModel) int {
	limit := modelInputBudget(target, planInputReserve) - minPageReserve
	return max(min(limit, maxBatchSourceChars), minSourceBatch)
}

// Worker 是有界的后台编译 Worker：单实例串行领取与发布，同一作用域的编译发布
// 天然串行。通知丢失也不丢任务，定时扫描和下次启动可发现待处理来源。
type Worker struct {
	svc      *Service
	lastLint time.Time
}

// lintInterval 是确定性完整性检查的周期。
const lintInterval = 24 * time.Hour

// forcedScopes 是用户点击“立即整理”登记的范围请求，由 Worker 循环消费；
// 领取始终发生在 Worker 内，避免与后台领取并发竞争同一批来源。
var forcedScopes = struct {
	sync.Mutex
	scopes map[string]bool
}{scopes: map[string]bool{}}

// RequestCompile 登记“立即整理”并唤醒 Worker；重复登记是幂等的。
func RequestCompile(scope string) {
	forcedScopes.Lock()
	forcedScopes.scopes[scope] = true
	forcedScopes.Unlock()
	Notify()
}

func popForcedScope() (string, bool) {
	forcedScopes.Lock()
	defer forcedScopes.Unlock()
	for scope := range forcedScopes.scopes {
		delete(forcedScopes.scopes, scope)
		return scope, true
	}
	return "", false
}

// NewWorker 装配记忆 Worker。
func NewWorker(svc *Service) *Worker { return &Worker{svc: svc} }

// Run 按防抖与重试排程循环处理；beginShutdown 停止新领取并取消当前远程调用，
// 退出前用短暂独立 context 保存必要状态。
func (w *Worker) Run(ctx context.Context) {
	// 启动回收：上次进程崩溃/强杀留下的 running 租约重新执行。
	w.recoverStaleJobs(ctx)
	for {
		delay := w.tick(ctx)
		if ctx.Err() != nil {
			w.shutdownSave()
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			w.shutdownSave()
			return
		case <-wake:
			stopTimer(timer)
		case <-timer.C:
		}
	}
}

// tick 执行一轮领取/处理并返回下一次唤醒间隔。
func (w *Worker) tick(ctx context.Context) time.Duration {
	next := w.processOnce(ctx)
	return min(max(time.Until(next), time.Second), baseRetryDelay)
}

// processOnce 处理一个到期批次（或回收一个到期作业），返回下一个事件时间。
func (w *Worker) processOnce(ctx context.Context) time.Time {
	policy, err := LoadPolicy(ctx, w.svc.client)
	if err != nil {
		w.svc.svcLogWarn("load memory worker policy failed", err)
		return nowTime().Add(batchMaxWait)
	}
	if !policy.Enabled {
		return nowTime().Add(batchMaxWait)
	}
	// 周期性确定性检查：孤立引用、已撤销来源、过期页面与可疑重复。
	if time.Since(w.lastLint) >= lintInterval {
		w.lastLint = time.Now()
		if stats, err := w.svc.LintPass(ctx); err != nil {
			w.svc.svcLogWarn("memory lint failed", err)
		} else if stats != (LintStats{}) {
			w.svc.logger.Info("memory lint stats",
				zap.Int("orphan_links", stats.OrphanLinks),
				zap.Int("excluded_evidence", stats.ExcludedEvidence),
				zap.Int("expired_pages", stats.ExpiredPages),
				zap.Int("duplicate_titles", stats.DuplicateTitles))
		}
	}
	if job := w.claimBackfillJob(ctx); job != nil {
		w.svc.runBackfill(ctx, job)
		return nowTime().Add(time.Second)
	}
	// 用户点击“立即整理”：跳过防抖，按登记范围处理一个批次。
	for {
		scope, ok := popForcedScope()
		if !ok {
			break
		}
		if job, _ := w.claimReadyBatch(ctx, scope); job != nil {
			w.svc.runJob(ctx, job)
			// 一次点击持续处理该范围的剩余批次，直到没有可领取来源。
			RequestCompile(scope)
			return nowTime().Add(time.Second)
		}
	}
	if job := w.claimDueJob(ctx); job != nil {
		w.svc.runJob(ctx, job)
		return nowTime().Add(time.Second)
	}
	if !policy.AutoCapture {
		return nowTime().Add(batchMaxWait)
	}
	if job, next := w.claimReadyBatch(ctx, ""); job != nil {
		w.svc.runJob(ctx, job)
		return nowTime().Add(time.Second)
	} else {
		if unblocked := w.unblockJobs(ctx); unblocked {
			return nowTime().Add(time.Second)
		}
		return next
	}
}

// CompileNow 立即整理已选择范围的待处理来源（用户点击“立即整理”）。
func (w *Worker) CompileNow(ctx context.Context, scope string) int {
	if err := ValidateScope(ctx, w.svc.client, scope); err != nil {
		return 0
	}
	created := 0
	for ctx.Err() == nil {
		job, _ := w.claimReadyBatch(ctx, scope)
		if job == nil {
			return created
		}
		created++
		w.svc.runJob(ctx, job)
	}
	return created
}

// claimDueJob 领取重试到期或租约过期的既有编译作业，不重新打包输入；
// 回填作业由 claimBackfillJob 处理，不在此领取。
func (w *Worker) claimDueJob(ctx context.Context) *ent.KaguyaMemoryJob {
	now := nowTime()
	job, err := w.svc.client.KaguyaMemoryJob.Query().
		Where(kaguyamemoryjob.KindEQ(kaguyamemoryjob.KindCompile),
			kaguyamemoryjob.Or(
				kaguyamemoryjob.And(
					kaguyamemoryjob.StatusEQ(kaguyamemoryjob.StatusRetryWait),
					kaguyamemoryjob.NextAttemptAtLTE(now),
				),
				kaguyamemoryjob.And(
					kaguyamemoryjob.StatusEQ(kaguyamemoryjob.StatusRunning),
					kaguyamemoryjob.LeaseExpiresAtLT(now),
				),
			)).
		Order(ent.Asc(kaguyamemoryjob.FieldCreatedAt)).First(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		w.svc.logger.Warn("query due memory job failed", zap.Error(err))
		return nil
	}
	if job.Attempt >= maxJobAttempts {
		// 达到重试上限：failed，不伪装成已处理。
		if err := w.svc.failJob(ctx, job, "attempts_exhausted", "memory job exceeded retry budget"); err != nil {
			w.svc.logger.Warn("fail memory job failed", zap.Error(err))
		}
		return nil
	}
	claimed, err := w.svc.claimJob(ctx, job)
	if err != nil {
		w.svc.logger.Warn("claim due memory job failed", zap.Error(err))
		return nil
	}
	return claimed
}

// claimReadyBatch 选定一个满足防抖条件的来源分组，创建并领取编译作业。
// 每批同一 conversation、同一 scope；输入集合在领取事务内冻结，正在运行的
// 批次输入固定，新增轮次进入下一批。
func (w *Worker) claimReadyBatch(ctx context.Context, scopeFilter string) (*ent.KaguyaMemoryJob, time.Time) {
	next := nowTime().Add(batchMaxWait)
	query := w.svc.client.KaguyaMemorySource.Query().Where(kaguyamemorysource.StateEQ(kaguyamemorysource.StatePending))
	// Filter before LIMIT: a backlog from disabled conversations must not
	// starve other conversations forever at the head of the queue.
	query.Where(func(selector *sql.Selector) {
		conv := sql.Table(kaguyaconversation.Table)
		selector.Where(sql.Or(
			sql.EQ(selector.C(kaguyamemorysource.FieldConversationID), ""),
			sql.Exists(sql.Select(conv.C(kaguyaconversation.FieldID)).From(conv).Where(sql.And(
				sql.ColumnsEQ(conv.C(kaguyaconversation.FieldID), selector.C(kaguyamemorysource.FieldConversationID)),
				sql.EQ(conv.C(kaguyaconversation.FieldMemoryMode), string(kaguyaconversation.MemoryModeInherit)),
				sql.IsNull(conv.C(kaguyaconversation.FieldDeletedAt)),
			))),
		))
	})
	if scopeFilter != "" {
		query.Where(kaguyamemorysource.ScopeKeyEQ(scopeFilter))
	}
	sources, err := query.Order(ent.Asc(kaguyamemorysource.FieldCapturedAt), ent.Asc(kaguyamemorysource.FieldID)).Limit(256).All(ctx)
	if err != nil {
		w.svc.logger.Warn("query pending memory sources failed", zap.Error(err))
		return nil, next
	}
	now := nowTime()
	type groupKey struct {
		conversation, scope string
	}
	groups := map[groupKey][]*ent.KaguyaMemorySource{}
	order := make([]groupKey, 0)
	for _, src := range sources {
		if scopeFilter != "" && src.ScopeKey != scopeFilter {
			continue
		}
		key := groupKey{src.ConversationID, src.ScopeKey}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], src)
	}
	for _, key := range order {
		group := groups[key]
		oldest := group[0].CapturedAt
		newest := group[len(group)-1].CapturedAt
		readyAt := newest.Add(batchQuietWindow)
		force := scopeFilter != "" || len(group) >= batchReadyCount || now.Sub(oldest) >= batchMaxWait
		if !force && now.Before(readyAt) {
			if readyAt.Before(next) {
				next = readyAt
			}
			continue
		}
		// 来源预算在领取时按任务模型窗口冻结，保证冻结输入可确定性重建。
		sourceBudget := maxBatchSourceChars
		if target, err := w.svc.taskModels(ctx, w.svc.client, group[0].ConversationID); err == nil {
			sourceBudget = sourceBudgetForModel(target)
		}
		job, err := w.svc.createJobForSources(ctx, group, sourceBudget)
		if err != nil {
			w.svc.logger.Warn("create memory job failed", zap.Error(err))
			continue
		}
		if job != nil && job.Status == kaguyamemoryjob.StatusRunning {
			return job, next
		}
	}
	return nil, next
}

// unblockJobs 在明确变化（Notify/预算重置）后恢复 blocked 作业。
func (w *Worker) unblockJobs(ctx context.Context) bool {
	blocked, err := w.svc.client.KaguyaMemoryJob.Query().
		Where(kaguyamemoryjob.StatusEQ(kaguyamemoryjob.StatusBlocked)).All(ctx)
	if err != nil || len(blocked) == 0 {
		return false
	}
	now := nowTime()
	for _, job := range blocked {
		switch job.ErrorCode {
		case "budget":
			// 日预算按 UTC 自然日重置。
			if now.UTC().Truncate(24 * time.Hour).Equal(job.UpdatedAt.UTC().Truncate(24 * time.Hour)) {
				continue
			}
		case "config":
			// 配置修复后由系统配置保存触发 Notify；未修复不重新领取或新增 Attempt。
			if job.Kind == kaguyamemoryjob.KindBackfill {
				policy, err := LoadPolicy(ctx, w.svc.client)
				if err != nil || !policy.Enabled {
					continue
				}
			} else if _, err := w.svc.taskModels(ctx, w.svc.client, job.ConversationID); err != nil {
				continue
			}
		default:
			continue
		}
		if err := w.svc.client.KaguyaMemoryJob.UpdateOneID(job.ID).
			SetStatus(kaguyamemoryjob.StatusRetryWait).SetNextAttemptAt(now).
			SetErrorCode("").SetErrorSummary("").Exec(ctx); err != nil {
			w.svc.logger.Warn("unblock memory job failed", zap.Error(err))
			continue
		}
		return true
	}
	return false
}

// recoverStaleJobs 回收过期 running 租约，重新执行；强杀来不及保存时的恢复入口。
func (w *Worker) recoverStaleJobs(ctx context.Context) {
	if err := w.svc.client.KaguyaMemoryJob.Update().
		Where(kaguyamemoryjob.StatusEQ(kaguyamemoryjob.StatusRunning)).
		SetStatus(kaguyamemoryjob.StatusRetryWait).SetNextAttemptAt(nowTime()).
		SetLeaseToken("").ClearLeaseExpiresAt().Exec(ctx); err != nil {
		w.svc.logger.Warn("recover memory jobs failed", zap.Error(err))
	}
}

// shutdownSave 关停阶段用短暂独立 context 把进行中的尝试标记为可重试。
func (w *Worker) shutdownSave() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.svc.client.KaguyaMemoryJob.Update().
		Where(kaguyamemoryjob.StatusEQ(kaguyamemoryjob.StatusRunning)).
		SetStatus(kaguyamemoryjob.StatusRetryWait).SetNextAttemptAt(nowTime()).
		SetLeaseToken("").ClearLeaseExpiresAt().Exec(ctx); err != nil {
		w.svc.logger.Warn("save memory job state during shutdown failed", zap.Error(err))
	}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

// createJobForSources 在一个短事务内：选定来源 → 创建 Job → 更新来源 job_id/state
// → 创建 Attempt → 提交。远程模型调用在事务之外执行。空投影来源标记 noop，
// 不生成空作业也不永久 pending。
func (s *Service) createJobForSources(ctx context.Context, group []*ent.KaguyaMemorySource, sourceBudget int) (*ent.KaguyaMemoryJob, error) {
	if sourceBudget <= 0 {
		sourceBudget = maxBatchSourceChars
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()

	projections, coveredIDs, emptyIDs, err := budgetProjections(ctx, client, group, sourceBudget)
	if err != nil {
		return nil, err
	}
	if len(emptyIDs) > 0 {
		// 没有可见分段：没有可提炼内容，标记 noop 而不是永久待处理。
		if _, err := client.KaguyaMemorySource.Update().
			Where(kaguyamemorysource.IDIn(emptyIDs...), kaguyamemorysource.StateEQ(kaguyamemorysource.StatePending)).
			SetState(kaguyamemorysource.StateNoop).Save(ctx); err != nil {
			return nil, err
		}
	}
	if len(coveredIDs) == 0 {
		return nil, tx.Commit()
	}
	payload, err := ProjectionPayload(projections)
	if err != nil {
		return nil, err
	}
	policy, err := LoadPolicy(ctx, client)
	if err != nil {
		return nil, err
	}
	if !policy.Enabled {
		return nil, nil
	}
	convPolicy, err := ResolveConversationPolicy(ctx, client, group[0].ConversationID, policy)
	if err != nil {
		return nil, err
	}
	if !convPolicy.Recall || convPolicy.Mode == ModeReadOnly {
		return nil, nil
	}
	if err := ValidateScope(ctx, client, group[0].ScopeKey); err != nil {
		return nil, err
	}
	// An indivisible legacy segment may exceed a small model's budget. Keep
	// the complete segment and surface a retryable configuration problem,
	// rather than leaving the source pending forever or losing its suffix.
	if len(projections) == 1 && len(projections[0].Segments) == 1 && len(projections[0].Segments[0].Text) > sourceBudget {
		job, err := blockGroupJob(ctx, client, group, coveredIDs, len(projections[0].Segments[0].Text), "input_budget", "source segment exceeds task model budget; select a larger context model and retry")
		if err != nil {
			return nil, err
		}
		return job, tx.Commit()
	}
	if err := checkDailyBudget(ctx, client); err != nil {
		// 预算耗尽：建 blocked 作业等待重置，而不是无限跑。
		job, blockErr := blockGroupJob(ctx, client, group, coveredIDs, sourceBudget, "budget", "daily memory budget exhausted")
		if blockErr != nil {
			return nil, blockErr
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return job, nil
	}

	job, err := client.KaguyaMemoryJob.Create().
		SetKind(kaguyamemoryjob.KindCompile).
		SetScopeKey(group[0].ScopeKey).SetConversationID(group[0].ConversationID).
		SetInputSourceIds(coveredIDs).
		SetInputHash(ProjectionHash([]Segment{{Text: string(payload)}})).
		SetSourceBudget(sourceBudget).
		SetCompilerVersion(CompilerVersion).
		SetStatus(kaguyamemoryjob.StatusRunning).
		SetAttempt(1).
		SetLeaseToken(newLeaseToken()).SetLeaseExpiresAt(nowTime().Add(leaseDuration)).
		SetPolicyEpoch(policy.Epoch).
		SetStartedAt(nowTime()).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	claimed, err := client.KaguyaMemorySource.Update().
		Where(kaguyamemorysource.IDIn(coveredIDs...), kaguyamemorysource.StateEQ(kaguyamemorysource.StatePending)).
		SetState(kaguyamemorysource.StateClaimed).SetJobID(job.ID).Save(ctx)
	if err != nil {
		return nil, err
	}
	if claimed != len(coveredIDs) {
		// 并发领取竞争：回滚，不能把同一个来源挂到两个作业。
		return nil, ErrStaleLease
	}
	// 领取即登记执行尝试；实际模型调用用量由 callTracked 补全到该行。
	if err := client.KaguyaMemoryAttempt.Create().
		SetJobID(job.ID).SetAttempt(job.Attempt).SetPhase(kaguyamemoryattempt.PhaseExtract).
		SetResultCode("started").Exec(ctx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return job, nil
}

// blockGroupJob 为分组建立 blocked 作业并领取来源，等待预算/配置明确变化。
func blockGroupJob(ctx context.Context, client *ent.Client, group []*ent.KaguyaMemorySource, coveredIDs []string, sourceBudget int, code, summary string) (*ent.KaguyaMemoryJob, error) {
	policy, err := LoadPolicy(ctx, client)
	if err != nil {
		return nil, err
	}
	job, err := client.KaguyaMemoryJob.Create().
		SetKind(kaguyamemoryjob.KindCompile).
		SetScopeKey(group[0].ScopeKey).SetConversationID(group[0].ConversationID).
		SetInputSourceIds(coveredIDs).
		SetSourceBudget(sourceBudget).
		SetCompilerVersion(CompilerVersion).
		SetPolicyEpoch(policy.Epoch).
		SetStatus(kaguyamemoryjob.StatusBlocked).
		SetAttempt(0).
		SetErrorCode(code).SetErrorSummary(summary).
		SetStartedAt(nowTime()).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	claimed, err := client.KaguyaMemorySource.Update().
		Where(kaguyamemorysource.IDIn(coveredIDs...), kaguyamemorysource.StateEQ(kaguyamemorysource.StatePending)).
		SetState(kaguyamemorysource.StateClaimed).SetJobID(job.ID).Save(ctx)
	if err != nil {
		return nil, err
	}
	if claimed != len(coveredIDs) {
		return nil, ErrStaleLease
	}
	return job, nil
}

// claimJob 领取既有作业的下一次执行尝试：新 lease token、attempt+1、新 Attempt。
func (s *Service) claimJob(ctx context.Context, job *ent.KaguyaMemoryJob) (*ent.KaguyaMemoryJob, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	lease := newLeaseToken()
	attempt := job.Attempt + 1
	if attempt < 1 {
		attempt = 1
	}
	updated, err := client.KaguyaMemoryJob.UpdateOneID(job.ID).
		Where(kaguyamemoryjob.StatusIn(kaguyamemoryjob.StatusRetryWait, kaguyamemoryjob.StatusRunning), kaguyamemoryjob.LeaseTokenEQ(job.LeaseToken)).
		SetStatus(kaguyamemoryjob.StatusRunning).
		SetAttempt(attempt).
		SetLeaseToken(lease).SetLeaseExpiresAt(nowTime().Add(leaseDuration)).
		SetStartedAt(nowTime()).Save(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrStaleLease
	}
	if err != nil {
		return nil, err
	}
	if err := client.KaguyaMemoryAttempt.Create().
		SetJobID(job.ID).SetAttempt(attempt).SetPhase(kaguyamemoryattempt.PhaseExtract).
		SetResultCode("started").Exec(ctx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	updated.Attempt = attempt
	updated.LeaseToken = lease
	updated.Status = kaguyamemoryjob.StatusRunning
	return updated, nil
}

// budgetProjections 按字节预算从游标构造有界投影；超预算的来源与分段留给下一批。
// 判定顺序与覆盖范围都按来源捕获顺序，保证冻结输入可确定性重建。
// 没有可见分段的来源单独返回，由调用方标记 noop，不能永久待处理。
func budgetProjections(ctx context.Context, client *ent.Client, group []*ent.KaguyaMemorySource, budget int) ([]*SourceProjection, []string, []string, error) {
	if budget <= 0 {
		budget = maxBatchSourceChars
	}
	projections := make([]*SourceProjection, 0, len(group))
	covered := make([]string, 0, len(group))
	empty := make([]string, 0)
	total := 0
	for _, src := range group {
		if total >= budget {
			break
		}
		segments, turnStatus, finishReason, err := loadSourceContent(ctx, client, src)
		if err != nil {
			return nil, nil, nil, err
		}
		// 内容哈希在 Worker 阶段异步推导；捕获事务不读投影。
		if err := deriveSourceHash(ctx, client, src, segments); err != nil {
			return nil, nil, nil, err
		}
		if len(segments) == 0 {
			empty = append(empty, src.ID)
			continue
		}
		projection := BuildSourceProjection(src, segments)
		projection.TurnStatus = turnStatus
		projection.FinishReason = finishReason
		// 不能截掉末尾后假装全部处理成功：按整段预算切分并记录覆盖范围。
		room := budget - total
		kept := make([]Segment, 0, len(projection.Segments))
		used := 0
		for _, segment := range projection.Segments {
			if used+len(segment.Text) > room {
				if len(projections) == 0 && len(kept) == 0 {
					kept = append(kept, segment)
					used = len(segment.Text)
				}
				break
			}
			used += len(segment.Text)
			kept = append(kept, segment)
		}
		if len(kept) == 0 {
			break
		}
		projection.Segments = kept
		total += used
		projections = append(projections, projection)
		covered = append(covered, src.ID)
	}
	return projections, covered, empty, nil
}

// checkDailyBudget 校验当日任务模型调用与 token 预算。
func checkDailyBudget(ctx context.Context, client *ent.Client) error {
	midnight := nowTime().UTC().Truncate(24 * time.Hour)
	calls, err := client.KaguyaMemoryAttempt.Query().
		Where(kaguyamemoryattempt.CreatedAtGTE(midnight)).Count(ctx)
	if err != nil {
		return err
	}
	if calls >= memoryDailyCalls {
		return errDailyBudget
	}
	rows, err := client.KaguyaMemoryAttempt.Query().
		Where(kaguyamemoryattempt.CreatedAtGTE(midnight), kaguyamemoryattempt.UsageKnownEQ(true)).
		Select(kaguyamemoryattempt.FieldID, kaguyamemoryattempt.FieldTotalTokens).All(ctx)
	if err != nil {
		return err
	}
	var tokens int64
	for _, row := range rows {
		tokens += row.TotalTokens
	}
	if tokens >= memoryDailyTokens {
		return errDailyBudget
	}
	return nil
}

// ErrJobNotFound 表示任务不存在。
var ErrJobNotFound = errors.New("memory job not found")

func newLeaseToken() string {
	return fmt.Sprintf("%d-%d", nowTime().UnixNano(), rand.Uint64())
}

// runJob 执行一次作业尝试：提炼 → 检索候选 → 合并提案 → 校验 → 原子发布。
// 所有分支都保存已知调用用量（callTracked 内完成），错误不伪装成 noop。
func (s *Service) runJob(ctx context.Context, job *ent.KaguyaMemoryJob) {
	if ctx.Err() != nil {
		s.retryLater(context.Background(), job, time.Second, "canceled")
		return
	}
	// 模型出站前也检查撤销栅栏，不能只在发布时拒绝已经泄露的资料。
	if err := s.checkJobAccess(ctx, job); err != nil {
		return
	}
	input, err := s.loadJobInput(ctx, job)
	if err != nil {
		s.svcLogWarn("load memory job input failed", err)
		_ = s.failJob(ctx, job, "input_mismatch", err.Error())
		return
	}
	target, err := s.taskModels(ctx, s.client, job.ConversationID)
	if err != nil {
		// 配置缺失/密钥不可用：blocked 等待用户修复和 Notify，不悄悄改用聊天模型。
		s.blockJob(job, "config", "task model unavailable")
		return
	}

	extracted, err := s.Extract(ctx, job, target, input)
	if err != nil {
		if isContractError(err) {
			s.invalidJob(ctx, job, "extract validation failed")
		} else {
			s.recordFailure(ctx, job, err)
		}
		return
	}
	projections := projectionsFromPayload(input)
	if len(extracted.Candidates) == 0 {
		// 合法空输出标记 noop：原子推进来源与作业状态，未覆盖分段仍待处理。
		s.noopJob(ctx, job, projections)
		return
	}
	candidates, err := s.findMergeCandidates(ctx, job.ScopeKey, extracted.Candidates)
	if err != nil {
		_ = s.failJob(ctx, job, "input_mismatch", "candidate lookup failed")
		return
	}
	// 阶段 C 按模型输入预算自动拆批；所有批次共用冻结候选集合并合并为一次发布。
	plan, err := s.planInBatches(ctx, job, target, input, extracted.Candidates, candidates)
	if err != nil {
		if errors.Is(err, ErrPlanInvalid) || isContractError(err) {
			s.invalidJob(ctx, job, "plan validation failed")
			return
		}
		s.recordFailure(ctx, job, err)
		return
	}
	if len(plan.Changes) == 0 {
		s.noopJob(ctx, job, projections)
		return
	}
	if _, err := s.Publish(ctx, job, plan, projections); err != nil {
		if errors.Is(err, ErrStaleLease) {
			return
		}
		s.recordFailure(ctx, job, err)
	}
}

func (s *Service) checkJobAccess(ctx context.Context, job *ent.KaguyaMemoryJob) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	release, err := checkLeaseAndSources(ctx, tx.Client(), job)
	if err != nil {
		return err
	}
	if release != nil {
		if err := release(ctx); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return ErrStaleLease
	}
	return tx.Commit()
}

// loadJobInput 确定性重建冻结输入并校验 input_hash；来源在 claimed 状态下不可变。
// 使用领取时冻结的来源预算，保证同一作业重试时投影完全一致。
func (s *Service) loadJobInput(ctx context.Context, job *ent.KaguyaMemoryJob) ([]byte, error) {
	sources, err := s.client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.IDIn(job.InputSourceIds...),
			kaguyamemorysource.StateEQ(kaguyamemorysource.StateClaimed),
			kaguyamemorysource.JobIDEQ(job.ID)).
		Order(ent.Asc(kaguyamemorysource.FieldCapturedAt), ent.Asc(kaguyamemorysource.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(sources) != len(job.InputSourceIds) {
		return nil, fmt.Errorf("memory job sources changed")
	}
	projections, covered, empty, err := budgetProjections(ctx, s.client, sources, job.SourceBudget)
	if err != nil {
		return nil, err
	}
	if len(empty) > 0 {
		return nil, fmt.Errorf("memory job source projection disappeared")
	}
	if len(covered) != len(job.InputSourceIds) {
		return nil, fmt.Errorf("memory job input coverage changed")
	}
	payload, err := ProjectionPayload(projections)
	if err != nil {
		return nil, err
	}
	if job.InputHash != "" && job.InputHash != ProjectionHash([]Segment{{Text: string(payload)}}) {
		return nil, fmt.Errorf("memory job input changed after claim")
	}
	return payload, nil
}

// projectionsFromPayload 从冻结输入恢复投影片段（发布时的允许证据集合）。
func projectionsFromPayload(payload []byte) []*SourceProjection {
	var projections []*SourceProjection
	if err := json.Unmarshal(payload, &projections); err != nil {
		return nil
	}
	return projections
}

// mergeCandidates 是阶段 B 的确定性候选检索结果：合并页面集合、每个候选命中的
// 页面优先级顺序，以及阶段 C 输入与证据校验共用的冻结修订快照。
type mergeCandidates struct {
	related          map[string]*ent.KaguyaMemoryPage
	pages            map[string]*ent.KaguyaMemoryPage
	pagesByCandidate map[string][]string
	data             map[string]candidatePageData
}

// candidatePageData 是候选页当前修订的冻结快照（阶段 C 输入与证据校验共用）。
type candidatePageData struct {
	relatedIDs []string
	claims     []dtomemory.MemoryClaim
	retained   []retainedEvidence
	rows       []*ent.KaguyaMemoryEvidence
}

// findMergeCandidates 对每个 candidate 做同 scope 的 canonical key 精确候选与
// FTS5 搜索（title + statement + aliases），取少量候选并加载完整小页面。
// 页面按“精确主题键优先、FTS 排名次之”的顺序记录，拆批收缩时优先保留。
func (s *Service) findMergeCandidates(ctx context.Context, scope string,
	candidates []dtomemory.Candidate) (*mergeCandidates, error) {
	out := &mergeCandidates{
		pages: map[string]*ent.KaguyaMemoryPage{}, pagesByCandidate: map[string][]string{},
		data: map[string]candidatePageData{}, related: map[string]*ent.KaguyaMemoryPage{},
	}
	add := func(page *ent.KaguyaMemoryPage, ids *[]string, seen map[string]bool) {
		if page == nil {
			return
		}
		if _, ok := out.pages[page.ID]; !ok {
			out.pages[page.ID] = page
		}
		if !seen[page.ID] {
			seen[page.ID] = true
			*ids = append(*ids, page.ID)
		}
	}
	for _, candidate := range candidates {
		ids := make([]string, 0)
		seen := map[string]bool{}
		pages, err := s.client.KaguyaMemoryPage.Query().
			Where(kaguyamemorypage.ScopeKeyEQ(scope),
				kaguyamemorypage.StatusNEQ(kaguyamemorypage.StatusDeleted),
				kaguyamemorypage.Or(
					kaguyamemorypage.CanonicalKeyEQ(strings.ToLower(candidate.Key)),
					kaguyamemorypage.CanonicalKeyEQ(deriveCanonicalKey(candidate.Title)),
				)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, page := range pages {
			add(page, &ids, seen)
		}
		hits, err := s.SearchPages(ctx, []string{scope}, candidate.Title+" "+candidate.Statement+" "+strings.Join(candidate.Aliases, " "), 5, true)
		if err != nil {
			return nil, err
		}
		for _, hit := range hits {
			hitPage, err := s.client.KaguyaMemoryPage.Get(ctx, hit.ID)
			if ent.IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			add(hitPage, &ids, seen)
		}
		out.pagesByCandidate[candidate.Key] = ids
	}
	// 冻结候选页当前修订与证据，所有批次共享，拆批不重复读取。
	for id, page := range out.pages {
		data, err := loadCandidatePageData(ctx, s.client, page)
		if err != nil {
			return nil, err
		}
		out.data[id] = data
		if len(data.relatedIDs) > 0 {
			linked, err := s.client.KaguyaMemoryPage.Query().Where(
				kaguyamemorypage.IDIn(data.relatedIDs...), kaguyamemorypage.ScopeKeyEQ(scope),
				kaguyamemorypage.DeletedAtIsNil(), kaguyamemorypage.StatusNEQ(kaguyamemorypage.StatusDeleted),
			).All(ctx)
			if err != nil {
				return nil, err
			}
			for _, row := range linked {
				out.related[row.ID] = row
			}
		}
	}
	return out, nil
}

// loadCandidatePageData 读取候选页当前修订的主张与证据行。
func loadCandidatePageData(ctx context.Context, client *ent.Client, page *ent.KaguyaMemoryPage) (candidatePageData, error) {
	var data candidatePageData
	links, err := client.KaguyaMemoryLink.Query().Where(kaguyamemorylink.FromPageIDEQ(page.ID), kaguyamemorylink.RelationEQ(kaguyamemorylink.RelationRelated)).Order(ent.Asc(kaguyamemorylink.FieldToPageID)).All(ctx)
	if err != nil {
		return data, err
	}
	data.relatedIDs = []string{}
	for _, link := range links {
		data.relatedIDs = append(data.relatedIDs, link.ToPageID)
	}
	revision, err := client.KaguyaMemoryRevision.Query().
		Where(kaguyamemoryrevision.PageIDEQ(page.ID), kaguyamemoryrevision.VersionEQ(page.Version)).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return data, err
	}
	if revision != nil {
		data.claims = revision.Claims
		data.rows, err = client.KaguyaMemoryEvidence.Query().
			Where(kaguyamemoryevidence.RevisionIDEQ(revision.ID)).All(ctx)
		if err != nil {
			return data, err
		}
	}
	data.retained = make([]retainedEvidence, 0, len(data.rows))
	for _, row := range data.rows {
		data.retained = append(data.retained, retainedEvidence{
			ClaimEvidence: dtomemory.ClaimEvidence{
				SourceID: row.SourceID, PartKey: row.PartKey, Quote: row.Quote, Relation: string(row.Relation),
			},
			Basis: string(row.Basis), ClaimKey: row.ClaimKey,
		})
	}
	return data, nil
}

func candidateKeySet(candidates []dtomemory.Candidate) map[string]bool {
	set := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		set[candidate.Key] = true
	}
	return set
}

// planBatch 是一次阶段 C 调用的候选与完整旧页面集合。
type planBatch struct {
	candidates []dtomemory.Candidate
	pages      map[string]*ent.KaguyaMemoryPage
}

// composePlanPayload 组装阶段 C 输入：冻结来源投影 + 候选主张 + 完整旧页面。
// 纯内存操作，拆批时用于精确测量输入大小，不含数据库读取；单个旧页面始终完整提供。
func composePlanPayload(scope string, input []byte, candidates []dtomemory.Candidate,
	pages map[string]*ent.KaguyaMemoryPage, data map[string]candidatePageData) ([]byte, error) {
	oldPages := make(map[string]any, len(pages))
	for id, page := range pages {
		claims := append([]dtomemory.MemoryClaim(nil), data[id].claims...)
		for i := range claims {
			claims[i].Statement = redactSecrets(claims[i].Statement)
		}
		evidence := make([]retainedEvidence, 0, len(data[id].retained))
		for _, ev := range data[id].retained {
			ev.Quote = redactSecrets(ev.Quote)
			evidence = append(evidence, ev)
		}
		aliases := make([]string, len(page.Aliases))
		for i, alias := range page.Aliases {
			aliases[i] = redactSecrets(alias)
		}
		oldPages[id] = map[string]any{
			"page_id": id, "version": page.Version, "canonical_key": redactSecrets(page.CanonicalKey),
			"kind": page.Kind, "title": redactSecrets(page.Title), "summary": redactSecrets(page.Summary),
			"body": redactSecrets(page.Body), "aliases": aliases,
			"evidence": evidence, "claims": claims, "related_ids": data[id].relatedIDs,
		}
	}
	return json.Marshal(map[string]any{
		"scope_key": scope, "sources": projectionsFromPayload(input),
		"candidates": candidates, "pages": oldPages,
	})
}

// splitPlanBatches 把候选按共享候选页分组装箱，保证每次阶段 C 调用输入不超过
// 模型可接受上限；单组件超预算时再按候选拆批，并优先保留更相关的候选页。
// 拆批顺序完全来自冻结输入，保证确定性且不会遗漏、重复或错误合并候选。
func splitPlanBatches(scope string, input []byte, candidates []dtomemory.Candidate,
	found *mergeCandidates, maxBytes int) ([]planBatch, error) {
	if maxBytes <= 0 {
		maxBytes = maxPlanInputBytes
	}
	// 共享候选页的候选必须同批处理，否则同一页面的 base_version 会在批间失效。
	parent := make(map[string]string, len(candidates))
	var find func(string) string
	find = func(key string) string {
		root, ok := parent[key]
		if !ok || root == key {
			parent[key] = key
			return key
		}
		parent[key] = find(root)
		return parent[key]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	shares := func(a, b []string) bool {
		set := make(map[string]bool, len(a))
		for _, id := range a {
			set[id] = true
		}
		for _, id := range b {
			if set[id] {
				return true
			}
		}
		return false
	}
	for i := range candidates {
		for j := i + 1; j < len(candidates); j++ {
			if shares(found.pagesByCandidate[candidates[i].Key], found.pagesByCandidate[candidates[j].Key]) {
				union(candidates[i].Key, candidates[j].Key)
			}
		}
	}
	componentOrder := make([]string, 0)
	components := map[string][]dtomemory.Candidate{}
	for _, candidate := range candidates {
		root := find(candidate.Key)
		if _, ok := components[root]; !ok {
			componentOrder = append(componentOrder, root)
		}
		components[root] = append(components[root], candidate)
	}

	var batches []planBatch
	current := planBatch{pages: map[string]*ent.KaguyaMemoryPage{}}
	currentKeys := map[string]bool{}
	compose := func(batch planBatch) ([]byte, error) {
		return composePlanPayload(scope, input, batch.candidates, batch.pages, found.data)
	}
	fits := func(batch planBatch) bool {
		payload, err := compose(batch)
		return err == nil && len(payload) <= maxBytes
	}
	merge := func(dst *planBatch, keys map[string]bool, cands []dtomemory.Candidate, pageIDs []string) {
		for _, candidate := range cands {
			if !keys[candidate.Key] {
				keys[candidate.Key] = true
				dst.candidates = append(dst.candidates, candidate)
			}
		}
		for _, id := range pageIDs {
			if _, ok := dst.pages[id]; !ok {
				dst.pages[id] = found.pages[id]
			}
		}
	}
	componentPages := func(comp []dtomemory.Candidate) []string {
		ids := make([]string, 0)
		seen := map[string]bool{}
		for _, candidate := range comp {
			for _, id := range found.pagesByCandidate[candidate.Key] {
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
		return ids
	}
	flush := func() {
		if len(current.candidates) == 0 {
			return
		}
		batches = append(batches, current)
		current = planBatch{pages: map[string]*ent.KaguyaMemoryPage{}}
		currentKeys = map[string]bool{}
	}
	for _, root := range componentOrder {
		comp := components[root]
		compPages := componentPages(comp)
		probe := planBatch{candidates: append([]dtomemory.Candidate(nil), current.candidates...), pages: map[string]*ent.KaguyaMemoryPage{}}
		for id, page := range current.pages {
			probe.pages[id] = page
		}
		probeKeys := map[string]bool{}
		for key := range currentKeys {
			probeKeys[key] = true
		}
		merge(&probe, probeKeys, comp, compPages)
		if fits(probe) {
			merge(&current, currentKeys, comp, compPages)
			continue
		}
		flush()
		if fits(planBatch{candidates: comp, pages: pagesFor(found, compPages)}) {
			merge(&current, currentKeys, comp, compPages)
			continue
		}
		// 单组件超预算：按候选拆批；单候选仍超预算时按优先级收缩候选页。
		for _, candidate := range comp {
			ids := found.pagesByCandidate[candidate.Key]
			probe = planBatch{candidates: append([]dtomemory.Candidate(nil), current.candidates...), pages: map[string]*ent.KaguyaMemoryPage{}}
			for id, page := range current.pages {
				probe.pages[id] = page
			}
			probeKeys = map[string]bool{}
			for key := range currentKeys {
				probeKeys[key] = true
			}
			merge(&probe, probeKeys, []dtomemory.Candidate{candidate}, ids)
			if fits(probe) {
				merge(&current, currentKeys, []dtomemory.Candidate{candidate}, ids)
				continue
			}
			flush()
			kept := shrinkPagesToFit(scope, input, candidate, ids, found, maxBytes)
			minimal := planBatch{candidates: []dtomemory.Candidate{candidate}, pages: kept}
			if !fits(minimal) {
				// 连最小批次都放不下：报告输入预算错误并保留来源，不伪装成成功。
				return nil, errInputBudget
			}
			merge(&current, currentKeys, []dtomemory.Candidate{candidate}, idsOf(kept))
		}
	}
	flush()
	if len(batches) == 0 {
		return nil, errInputBudget
	}
	return batches, nil
}

func pagesFor(found *mergeCandidates, ids []string) map[string]*ent.KaguyaMemoryPage {
	pages := make(map[string]*ent.KaguyaMemoryPage, len(ids))
	for _, id := range ids {
		pages[id] = found.pages[id]
	}
	return pages
}

func idsOf(pages map[string]*ent.KaguyaMemoryPage) []string {
	ids := make([]string, 0, len(pages))
	for id := range pages {
		ids = append(ids, id)
	}
	return ids
}

// shrinkPagesToFit 按候选页优先级逐个尝试加入，直到单次输入预算耗尽；
// 至少保留零页也不截断页面正文。
func shrinkPagesToFit(scope string, input []byte, candidate dtomemory.Candidate, pageIDs []string,
	found *mergeCandidates, maxBytes int) map[string]*ent.KaguyaMemoryPage {
	kept := map[string]*ent.KaguyaMemoryPage{}
	for _, id := range pageIDs {
		kept[id] = found.pages[id]
		payload, err := composePlanPayload(scope, input, []dtomemory.Candidate{candidate}, kept, found.data)
		if err != nil || len(payload) > maxBytes {
			delete(kept, id)
			break
		}
	}
	return kept
}

// planInBatches 按模型输入预算把候选拆成多个阶段 C 调用，逐批校验后合并为
// 单个 PatchPlan；所有批次在同一发布事务中提交，失败整体回滚。
func (s *Service) planInBatches(ctx context.Context, job *ent.KaguyaMemoryJob, target *servicesystem.TaskModel,
	input []byte, candidates []dtomemory.Candidate, found *mergeCandidates) (*dtomemory.PatchPlan, error) {
	projections := projectionsFromPayload(input)
	batches, err := splitPlanBatches(job.ScopeKey, input, candidates, found, modelInputBudget(target, planInputReserve))
	if err != nil {
		return nil, err
	}
	combined := &dtomemory.PatchPlan{SchemaVersion: dtomemory.ContractSchemaVersion}
	for _, batch := range batches {
		payload, err := composePlanPayload(job.ScopeKey, input, batch.candidates, batch.pages, found.data)
		if err != nil {
			return nil, err
		}
		// 生成和修复共用生成前冻结的候选集，不扩大页面与证据权限。
		index := NewEvidenceIndex(projections)
		for id := range batch.pages {
			index.AddRetained(id, found.data[id].rows)
		}
		related := make(map[string]*ent.KaguyaMemoryPage, len(batch.pages)+len(found.related))
		for id, page := range batch.pages {
			related[id] = page
		}
		for id, page := range found.related {
			related[id] = page
		}
		plan, err := s.Plan(ctx, job, target, payload, func(plan *dtomemory.PatchPlan) error {
			return ValidatePlan(ValidatePlanInput{
				SchemaVersionScope: job.ScopeKey, Projections: projections,
				CandidatePages: batch.pages, RelatedPages: related,
				CandidateKeys: candidateKeySet(batch.candidates), Evidence: index, Plan: plan,
			})
		})
		if err != nil {
			return nil, err
		}
		combined.Changes = append(combined.Changes, plan.Changes...)
	}
	return combined, nil
}

// recordFailure 分类失败：认证/预算 blocked，瞬时错误指数退避重试，
// 其余达到上限后 failed；来源不伪装成已处理。
func (s *Service) recordFailure(ctx context.Context, job *ent.KaguyaMemoryJob, cause error) {
	if errors.Is(cause, ErrStaleLease) {
		return
	}
	s.logger.Warn("memory compilation failed", zap.String("job_id", job.ID), zap.String("error_code", classifyCode(cause)), zap.Int("attempt", job.Attempt))
	if callBlocked(cause) {
		s.blockJob(job, classifyCode(cause), cause.Error())
		return
	}
	if callRetryable(cause) && job.Attempt < maxJobAttempts {
		delay, code := retryDelay(job.Attempt, cause)
		s.retryLater(ctx, job, delay, code)
		return
	}
	_ = s.failJob(ctx, job, classifyCode(cause), cause.Error())
}

// classifyCode 映射上游错误为安全错误码。
func classifyCode(cause error) string {
	code, _ := classifyCallError(cause)
	return code
}

// retryDelay 计算指数退避加 jitter 的重试间隔，尊重可用的 Retry-After。
func retryDelay(attempt int, cause error) (time.Duration, string) {
	if _, retryAfter := classifyCallError(cause); retryAfter > 0 {
		return retryAfter, "rate_limited"
	}
	code := classifyCode(cause)
	if attempt < 1 {
		attempt = 1
	}
	delay := baseRetryDelay << (attempt - 1)
	jitter := time.Duration(rand.Int64N(int64(delay/2) + 1))
	return delay + jitter, code
}

func (s *Service) retryLater(ctx context.Context, job *ent.KaguyaMemoryJob, delay time.Duration, code string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.client.KaguyaMemoryJob.UpdateOneID(job.ID).
		Where(kaguyamemoryjob.StatusEQ(kaguyamemoryjob.StatusRunning), kaguyamemoryjob.LeaseTokenEQ(job.LeaseToken)).
		SetStatus(kaguyamemoryjob.StatusRetryWait).
		SetNextAttemptAt(nowTime().Add(delay)).
		SetLeaseToken("").ClearLeaseExpiresAt().
		SetErrorCode(code).Exec(ctx); err != nil {
		s.svcLogWarn("schedule memory job retry failed", err)
	}
}

func (s *Service) blockJob(job *ent.KaguyaMemoryJob, code, summary string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 未产生模型调用的 blocked 不消耗执行尝试上限。
	attempt := max(job.Attempt-1, 0)
	if err := s.client.KaguyaMemoryJob.UpdateOneID(job.ID).
		Where(kaguyamemoryjob.StatusEQ(kaguyamemoryjob.StatusRunning), kaguyamemoryjob.LeaseTokenEQ(job.LeaseToken)).
		SetStatus(kaguyamemoryjob.StatusBlocked).
		SetAttempt(attempt).
		SetLeaseToken("").ClearLeaseExpiresAt().
		SetNextAttemptAt(nowTime().Add(24 * time.Hour)).
		SetErrorCode(code).SetErrorSummary(code).Exec(ctx); err != nil {
		s.svcLogWarn("block memory job failed", err)
	}
}

// failJob 记录终态失败：来源标 failed 可人工重试，不伪装成已处理。
func (s *Service) failJob(ctx context.Context, job *ent.KaguyaMemoryJob, code, summary string) error {
	if ctx == nil || ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	if err := client.KaguyaMemorySource.Update().
		Where(kaguyamemorysource.IDIn(job.InputSourceIds...), kaguyamemorysource.StateEQ(kaguyamemorysource.StateClaimed)).
		SetState(kaguyamemorysource.StateFailed).Exec(ctx); err != nil {
		return err
	}
	if err := client.KaguyaMemoryJob.UpdateOneID(job.ID).
		Where(kaguyamemoryjob.StatusEQ(job.Status), kaguyamemoryjob.LeaseTokenEQ(job.LeaseToken)).
		SetStatus(kaguyamemoryjob.StatusFailed).
		SetLeaseToken("").ClearLeaseExpiresAt().
		SetErrorCode(code).SetErrorSummary(code).
		SetFinishedAt(nowTime()).Exec(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

// invalidJob 记录校验失败：JSON 解析失败、来源校验失败不能当作 noop。
func (s *Service) invalidJob(ctx context.Context, job *ent.KaguyaMemoryJob, summary string) {
	_ = s.failJob(ctx, job, "invalid_output", summary)
}

// noopJob 原子标记合法空输出：来源 noop（未覆盖分段仍待处理），作业成功。
func (s *Service) noopJob(ctx context.Context, job *ent.KaguyaMemoryJob, projections []*SourceProjection) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		s.svcLogWarn("noop memory job failed", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	release, err := checkLeaseAndSources(ctx, client, job)
	if err != nil {
		return
	}
	if release != nil {
		if err := release(ctx); err != nil {
			s.svcLogWarn("release stale noop job failed", err)
			return
		}
		if err := tx.Commit(); err != nil {
			s.svcLogWarn("release stale noop job failed", err)
		}
		return
	}
	if err := completeSourcesTx(ctx, client, job, projections, PublishResult{}); err != nil {
		s.svcLogWarn("noop memory job failed", err)
		return
	}
	summary, err := json.Marshal(map[string]int{"noop": 1})
	if err != nil {
		s.svcLogWarn("noop memory job failed", err)
		return
	}
	if err := client.KaguyaMemoryJob.UpdateOneID(job.ID).
		SetStatus(kaguyamemoryjob.StatusSucceeded).
		SetResultJSON(string(summary)).
		SetLeaseToken("").ClearLeaseExpiresAt().
		SetFinishedAt(nowTime()).Exec(ctx); err != nil {
		s.svcLogWarn("noop memory job failed", err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.svcLogWarn("noop memory job failed", err)
	}
}

// RetryJob 人工重试失败/阻塞的作业：来源恢复为 claimed 重新执行。
func (s *Service) RetryJob(ctx context.Context, id string) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	job, err := client.KaguyaMemoryJob.Get(ctx, id)
	if ent.IsNotFound(err) {
		return ErrJobNotFound
	}
	if err != nil {
		return err
	}
	switch job.Status {
	case kaguyamemoryjob.StatusFailed, kaguyamemoryjob.StatusBlocked, kaguyamemoryjob.StatusCanceled:
	default:
		return ErrStaleLease
	}
	if err := client.KaguyaMemorySource.Update().
		Where(kaguyamemorysource.IDIn(job.InputSourceIds...),
			kaguyamemorysource.StateIn(kaguyamemorysource.StateFailed, kaguyamemorysource.StatePending)).
		SetState(kaguyamemorysource.StateClaimed).SetJobID(job.ID).Exec(ctx); err != nil {
		return err
	}
	// 人工重试重新给足执行尝试预算。
	if err := client.KaguyaMemoryJob.UpdateOneID(job.ID).
		SetStatus(kaguyamemoryjob.StatusRetryWait).SetAttempt(0).
		SetNextAttemptAt(nowTime()).SetErrorCode("").SetErrorSummary("").
		ClearFinishedAt().Exec(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	Notify()
	return nil
}

func (s *Service) svcLogWarn(msg string, err error) {
	s.logger.Warn(msg, zap.Error(err))
}

// ListJobs 分页查询任务状态、错误码与成本；不返回原始 Prompt，
// 待审作业附带保存的有界 PatchPlan 供界面审阅。
func (s *Service) ListJobs(ctx context.Context, req *dtomemory.MemoryJobListReq) (*dtomemory.MemoryJobListResp, error) {
	query := s.client.KaguyaMemoryJob.Query()
	if req.Status != "" {
		query.Where(kaguyamemoryjob.StatusEQ(kaguyamemoryjob.Status(req.Status)))
	}
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := query.Order(ent.Desc(kaguyamemoryjob.FieldCreatedAt)).
		Offset((req.Page - 1) * req.PageSize).Limit(req.PageSize).All(ctx)
	if err != nil {
		return nil, err
	}
	resp := &dtomemory.MemoryJobListResp{
		Total: total, Page: req.Page, PageSize: req.PageSize,
		Items: make([]dtomemory.MemoryJobResp, 0, len(rows)),
	}
	for _, row := range rows {
		item := dtomemory.MemoryJobResp{
			ID: row.ID, Kind: string(row.Kind), ScopeKey: row.ScopeKey, ConversationID: row.ConversationID,
			Status: string(row.Status), Attempt: row.Attempt,
			ErrorCode: row.ErrorCode, ErrorSummary: row.ErrorSummary,
			CreatedAt: row.CreatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
		}
		if row.Status == kaguyamemoryjob.StatusNeedsReview && row.ResultJSON != "" {
			if plan, err := reviewPlan(row); err == nil {
				item.Proposal = plan
			}
		}
		if row.Kind == kaguyamemoryjob.KindBackfill {
			item.Progress = backfillJobProgress(row)
		}
		attempts, err := s.client.KaguyaMemoryAttempt.Query().
			Where(kaguyamemoryattempt.JobIDEQ(row.ID)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, attempt := range attempts {
			item.Calls++
			if !attempt.UsageKnown {
				continue
			}
			item.InputTokens += attempt.InputTokens
			item.OutputTokens += attempt.OutputTokens
			item.TotalTokens += attempt.TotalTokens
		}
		resp.Items = append(resp.Items, item)
	}
	return resp, nil
}

// ApproveJob 批准待审提案：以当前页面版本发布新修订。
func (s *Service) ApproveJob(ctx context.Context, id string) (PublishResult, error) {
	job, err := s.client.KaguyaMemoryJob.Get(ctx, id)
	if ent.IsNotFound(err) {
		return PublishResult{}, ErrJobNotFound
	}
	if err != nil {
		return PublishResult{}, err
	}
	return s.ApproveReview(ctx, job)
}

// RejectJob 拒绝待审提案并记录处理结果。
func (s *Service) RejectJob(ctx context.Context, id string) error {
	job, err := s.client.KaguyaMemoryJob.Get(ctx, id)
	if ent.IsNotFound(err) {
		return ErrJobNotFound
	}
	if err != nil {
		return err
	}
	return s.RejectReview(ctx, job)
}
