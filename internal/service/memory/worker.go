package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryattempt"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryjob"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorypage"
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
	maxJobAttempts      = 3
	leaseDuration       = 10 * time.Minute
	// 日预算：达到后 blocked 而不是无限跑。
	memoryDailyCalls  = 400
	memoryDailyTokens = 2_000_000
	baseRetryDelay    = 30 * time.Second
)

// Worker 是有界的后台编译 Worker：单实例串行领取与发布，同一作用域的编译发布
// 天然串行。通知丢失也不丢任务，定时扫描和下次启动可发现待处理来源。
type Worker struct {
	svc *Service
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
	return max(time.Until(next), time.Second)
}

// processOnce 处理一个到期批次（或回收一个到期作业），返回下一个事件时间。
func (w *Worker) processOnce(ctx context.Context) time.Time {
	if job := w.claimDueJob(ctx); job != nil {
		w.svc.runJob(ctx, job)
		return nowTime().Add(time.Second)
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

// claimDueJob 领取重试到期或租约过期的既有作业，不重新打包输入。
func (w *Worker) claimDueJob(ctx context.Context) *ent.KaguyaMemoryJob {
	now := nowTime()
	job, err := w.svc.client.KaguyaMemoryJob.Query().
		Where(kaguyamemoryjob.Or(
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
	sources, err := w.svc.client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.StateEQ(kaguyamemorysource.StatePending)).
		Order(ent.Asc(kaguyamemorysource.FieldCapturedAt)).Limit(256).All(ctx)
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
		job, err := w.svc.createJobForSources(ctx, group)
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
			if now.Sub(job.UpdatedAt) < 24*time.Hour {
				continue
			}
		case "config":
			// 配置修复后由系统配置保存触发 Notify；这里做一次复核。
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
// → 创建 Attempt → 提交。远程模型调用在事务之外执行。
func (s *Service) createJobForSources(ctx context.Context, group []*ent.KaguyaMemorySource) (*ent.KaguyaMemoryJob, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()

	projections, coveredIDs, err := budgetProjections(ctx, client, group)
	if err != nil || len(coveredIDs) == 0 {
		return nil, err
	}
	payload, err := ProjectionPayload(projections)
	if err != nil {
		return nil, err
	}
	policy, err := LoadPolicy(ctx, client)
	if err != nil {
		return nil, err
	}
	if err := checkDailyBudget(ctx, client); err != nil {
		// 预算耗尽：建 blocked 作业等待重置，而不是无限跑。
		job, blockErr := blockGroupJob(ctx, client, group, coveredIDs, "budget", "daily memory budget exhausted")
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
	if err := client.KaguyaMemorySource.Update().
		Where(kaguyamemorysource.IDIn(coveredIDs...), kaguyamemorysource.StateEQ(kaguyamemorysource.StatePending)).
		SetState(kaguyamemorysource.StateClaimed).SetJobID(job.ID).Exec(ctx); err != nil {
		return nil, err
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
func blockGroupJob(ctx context.Context, client *ent.Client, group []*ent.KaguyaMemorySource, coveredIDs []string, code, summary string) (*ent.KaguyaMemoryJob, error) {
	job, err := client.KaguyaMemoryJob.Create().
		SetKind(kaguyamemoryjob.KindCompile).
		SetScopeKey(group[0].ScopeKey).SetConversationID(group[0].ConversationID).
		SetInputSourceIds(coveredIDs).
		SetCompilerVersion(CompilerVersion).
		SetStatus(kaguyamemoryjob.StatusBlocked).
		SetAttempt(0).
		SetErrorCode(code).SetErrorSummary(summary).
		SetStartedAt(nowTime()).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	if err := client.KaguyaMemorySource.Update().
		Where(kaguyamemorysource.IDIn(coveredIDs...), kaguyamemorysource.StateEQ(kaguyamemorysource.StatePending)).
		SetState(kaguyamemorysource.StateClaimed).SetJobID(job.ID).Exec(ctx); err != nil {
		return nil, err
	}
	return job, nil
}

// claimJob 领取既有作业的下一次执行尝试：新 lease token、attempt+1、新 Attempt。
func (s *Service) claimJob(ctx context.Context, job *ent.KaguyaMemoryJob) (*ent.KaguyaMemoryJob, error) {
	lease := newLeaseToken()
	attempt := job.Attempt + 1
	if attempt < 1 {
		attempt = 1
	}
	updated, err := s.client.KaguyaMemoryJob.UpdateOneID(job.ID).
		Where(kaguyamemoryjob.StatusIn(kaguyamemoryjob.StatusRetryWait, kaguyamemoryjob.StatusRunning)).
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
	if err := s.client.KaguyaMemoryAttempt.Create().
		SetJobID(job.ID).SetAttempt(attempt).SetPhase(kaguyamemoryattempt.PhaseExtract).
		SetResultCode("started").Exec(ctx); err != nil {
		return nil, err
	}
	updated.Attempt = attempt
	updated.LeaseToken = lease
	updated.Status = kaguyamemoryjob.StatusRunning
	return updated, nil
}

// budgetProjections 按字符预算从游标构造有界投影；超预算的来源与分段留给下一批。
// 判定顺序与覆盖范围都按来源捕获顺序，保证冻结输入可确定性重建。
func budgetProjections(ctx context.Context, client *ent.Client, group []*ent.KaguyaMemorySource) ([]*SourceProjection, []string, error) {
	projections := make([]*SourceProjection, 0, len(group))
	covered := make([]string, 0, len(group))
	total := 0
	for _, src := range group {
		if total >= maxBatchSourceChars {
			break
		}
		projection, err := LoadSourceProjection(ctx, client, src)
		if err != nil {
			return nil, nil, err
		}
		if len(projection.Segments) == 0 {
			continue
		}
		// 不能截掉末尾后假装全部处理成功：按整段预算切分并记录覆盖范围。
		room := maxBatchSourceChars - total
		kept := make([]Segment, 0, len(projection.Segments))
		used := 0
		for _, segment := range projection.Segments {
			if used+len(segment.Text) > room {
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
	return projections, covered, nil
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
		return errors.New("daily memory call budget exhausted")
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
		return errors.New("daily memory token budget exhausted")
	}
	return nil
}

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
		// 格式错误最多一次有界修复，总调用次数仍受预算控制。
		var broken *contractError
		if errors.As(err, &broken) {
			extracted, err = s.repairExtract(ctx, job, target, broken.raw)
		}
		if err != nil {
			if isContractError(err) {
				s.invalidJob(ctx, job, "invalid extract JSON")
				return
			}
			s.recordFailure(ctx, job, err)
			return
		}
	}
	if extracted.SchemaVersion != dtomemory.ContractSchemaVersion {
		s.invalidJob(ctx, job, "schema_version mismatch")
		return
	}
	projections := projectionsFromPayload(input)
	if len(extracted.Candidates) == 0 {
		// 合法空输出标记 noop：原子推进来源与作业状态，未覆盖分段仍待处理。
		s.noopJob(ctx, job, projections)
		return
	}
	payload, err := s.buildPlanPayload(job.ScopeKey, input, extracted.Candidates)
	if err != nil {
		_ = s.failJob(ctx, job, "input_mismatch", err.Error())
		return
	}
	plan, err := s.Plan(ctx, job, target, payload)
	if err != nil {
		var broken *contractError
		if errors.As(err, &broken) {
			plan, err = s.repairPlan(ctx, job, target, broken.raw)
		}
		if err != nil {
			if isContractError(err) {
				s.invalidJob(ctx, job, "invalid plan JSON")
				return
			}
			s.recordFailure(ctx, job, err)
			return
		}
	}
	candidates, err := s.findMergeCandidates(ctx, job.ScopeKey, extracted.Candidates, plan)
	if err != nil {
		_ = s.failJob(ctx, job, "input_mismatch", err.Error())
		return
	}
	index := NewEvidenceIndex(projections)
	if err := LoadRetainedEvidence(ctx, s.client, index, candidates.pages); err != nil {
		_ = s.failJob(ctx, job, "input_mismatch", err.Error())
		return
	}
	if err := ValidatePlan(ValidatePlanInput{
		SchemaVersionScope: job.ScopeKey,
		Projections:        projections,
		CandidatePages:     candidates.pages,
		RelatedPages:       candidates.related,
		CandidateKeys:      candidateKeySet(extracted.Candidates),
		Evidence:           index,
		Plan:               plan,
	}); err != nil {
		s.invalidJob(ctx, job, "plan validation failed")
		return
	}
	if _, err := s.Publish(ctx, job, plan, projections); err != nil {
		if errors.Is(err, ErrStaleLease) {
			return
		}
		s.recordFailure(ctx, job, err)
	}
}

// loadJobInput 确定性重建冻结输入并校验 input_hash；来源在 claimed 状态下不可变。
func (s *Service) loadJobInput(ctx context.Context, job *ent.KaguyaMemoryJob) ([]byte, error) {
	sources, err := s.client.KaguyaMemorySource.Query().
		Where(kaguyamemorysource.IDIn(job.InputSourceIds...),
			kaguyamemorysource.StateEQ(kaguyamemorysource.StateClaimed),
			kaguyamemorysource.JobIDEQ(job.ID)).
		Order(ent.Asc(kaguyamemorysource.FieldCapturedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(sources) != len(job.InputSourceIds) {
		return nil, fmt.Errorf("memory job sources changed")
	}
	projections, covered, err := budgetProjections(ctx, s.client, sources)
	if err != nil {
		return nil, err
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

// mergeCandidates 是阶段 B 的确定性候选检索结果。
type mergeCandidates struct {
	pages   map[string]*ent.KaguyaMemoryPage
	related map[string]*ent.KaguyaMemoryPage
}

// findMergeCandidates 对每个 candidate 做同 scope 的 canonical key 精确候选与
// FTS5 搜索（title + statement + aliases），取少量候选并加载完整小页面。
func (s *Service) findMergeCandidates(ctx context.Context, scope string,
	candidates []dtomemory.Candidate, plan *dtomemory.PatchPlan) (*mergeCandidates, error) {
	out := &mergeCandidates{pages: map[string]*ent.KaguyaMemoryPage{}, related: map[string]*ent.KaguyaMemoryPage{}}
	add := func(page *ent.KaguyaMemoryPage) {
		if page != nil && out.pages[page.ID] == nil {
			out.pages[page.ID] = page
		}
	}
	for _, candidate := range candidates {
		page, err := s.client.KaguyaMemoryPage.Query().
			Where(kaguyamemorypage.ScopeKeyEQ(scope),
				kaguyamemorypage.StatusNEQ(kaguyamemorypage.StatusDeleted),
				kaguyamemorypage.Or(
					kaguyamemorypage.CanonicalKeyEQ(strings.ToLower(candidate.Key)),
					kaguyamemorypage.CanonicalKeyEQ(deriveCanonicalKey(candidate.Title)),
				)).Only(ctx)
		if err == nil {
			add(page)
		} else if !ent.IsNotFound(err) {
			return nil, err
		}
		hits, err := s.SearchPages(ctx, []string{scope}, candidate.Title+" "+candidate.Statement, 5, true)
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
			add(hitPage)
		}
	}
	for i := range plan.Changes {
		for _, related := range plan.Changes[i].RelatedIDs {
			if out.related[related] != nil {
				continue
			}
			page, err := s.client.KaguyaMemoryPage.Get(ctx, related)
			if ent.IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			out.related[related] = page
		}
	}
	return out, nil
}

func candidateKeySet(candidates []dtomemory.Candidate) map[string]bool {
	set := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		set[candidate.Key] = true
	}
	return set
}

// buildPlanPayload 组装阶段 C 输入：冻结来源投影 + 候选主张 + 少量完整旧页面。
func (s *Service) buildPlanPayload(scope string, input []byte, candidates []dtomemory.Candidate) ([]byte, error) {
	return json.Marshal(map[string]any{
		"scope_key":  scope,
		"sources":    projectionsFromPayload(input),
		"candidates": candidates,
	})
}

// repairExtract / repairPlan 各执行一次有界格式修复。
func (s *Service) repairExtract(ctx context.Context, job *ent.KaguyaMemoryJob, target *servicesystem.TaskModel, broken string) (*dtomemory.ExtractResult, error) {
	raw, err := s.Repair(ctx, job, target, broken)
	if err != nil {
		return nil, err
	}
	var extracted dtomemory.ExtractResult
	if err := strictDecodeJSON(raw, &extracted); err != nil {
		return nil, &contractError{raw: raw, err: err}
	}
	return &extracted, nil
}

func (s *Service) repairPlan(ctx context.Context, job *ent.KaguyaMemoryJob, target *servicesystem.TaskModel, broken string) (*dtomemory.PatchPlan, error) {
	raw, err := s.Repair(ctx, job, target, broken)
	if err != nil {
		return nil, err
	}
	var plan dtomemory.PatchPlan
	if err := strictDecodeJSON(raw, &plan); err != nil {
		return nil, &contractError{raw: raw, err: err}
	}
	return &plan, nil
}

// recordFailure 分类失败：认证/预算 blocked，瞬时错误指数退避重试，
// 其余达到上限后 failed；来源不伪装成已处理。
func (s *Service) recordFailure(ctx context.Context, job *ent.KaguyaMemoryJob, cause error) {
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
	if err := s.client.KaguyaMemoryJob.UpdateOneID(job.ID).
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
		SetStatus(kaguyamemoryjob.StatusBlocked).
		SetAttempt(attempt).
		SetLeaseToken("").ClearLeaseExpiresAt().
		SetNextAttemptAt(nowTime().Add(24 * time.Hour)).
		SetErrorCode(code).SetErrorSummary(truncateRunes(summary, 500)).Exec(ctx); err != nil {
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
		SetStatus(kaguyamemoryjob.StatusFailed).
		SetLeaseToken("").ClearLeaseExpiresAt().
		SetErrorCode(code).SetErrorSummary(truncateRunes(summary, 500)).
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
