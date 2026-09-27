package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryjob"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
	"go.uber.org/zap"
)

// backfillPageSize 是每次 Worker 领取处理的轮次分页大小；分页与游标保证
// 大数据量回填可以中断、恢复和幂等重跑。
const backfillPageSize = 100

// defaultBackfillMaxSources 是未显式给成本上限时的保守默认值。
const defaultBackfillMaxSources = 500

// backfillState 是 backfill 作业 result_json 中的配置与进度。
// 游标使用轮次 ID（Sonyflake 时间有序），重跑按游标继续，已入队来源由
// source_key 唯一约束过滤，不会重复捕获。
type backfillState struct {
	Config   backfillConfig `json:"config"`
	Cursor   string         `json:"cursor,omitempty"`
	Scanned  int            `json:"scanned"`
	Created  int            `json:"created"`
	Skipped  int            `json:"skipped"`
	Limited  bool           `json:"limited,omitempty"`
	Finished bool           `json:"finished,omitempty"`
}

type backfillConfig struct {
	ScopeKey       string     `json:"scope_key"`
	ConversationID string     `json:"conversation_id,omitempty"`
	After          *time.Time `json:"after,omitempty"`
	Before         *time.Time `json:"before,omitempty"`
	MaxSources     int        `json:"max_sources"`
}

func encodeBackfillState(state backfillState) (string, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	if len(raw) > 64<<10 {
		return "", fmt.Errorf("%w: backfill state exceeds size limit", ErrPlanInvalid)
	}
	return string(raw), nil
}

func decodeBackfillState(job *ent.KaguyaMemoryJob) (backfillState, error) {
	var state backfillState
	if strings.TrimSpace(job.ResultJSON) == "" {
		return state, errors.New("backfill job has no state")
	}
	if err := json.Unmarshal([]byte(job.ResultJSON), &state); err != nil {
		return state, err
	}
	return state, nil
}

// StartBackfill 显式登记用户选择范围与成本上限的历史回填。同一范围已有
// 进行中作业时幂等返回该作业，不重复扫描。
func (s *Service) StartBackfill(ctx context.Context, req *dtomemory.MemoryBackfillReq) (*dtomemory.MemoryBackfillResp, error) {
	if req.MaxSources <= 0 {
		req.MaxSources = defaultBackfillMaxSources
	}
	if err := ValidateScope(ctx, s.client, req.ScopeKey); err != nil {
		return nil, err
	}
	if req.ScopeKey == ScopeShared {
		return nil, fmt.Errorf("%w: shared scope has no conversation history to backfill", ErrPlanInvalid)
	}
	if req.After != nil && req.Before != nil && !req.After.Before(*req.Before) {
		return nil, fmt.Errorf("%w: invalid backfill time range", ErrPlanInvalid)
	}
	policy, err := LoadPolicy(ctx, s.client)
	if err != nil {
		return nil, err
	}
	if !policy.Enabled {
		return nil, fmt.Errorf("%w: memory is disabled", ErrPlanInvalid)
	}
	if req.ConversationID != "" {
		if _, err := s.conversationInScope(ctx, req.ScopeKey, req.ConversationID); err != nil {
			return nil, err
		}
	}
	existing, err := s.client.KaguyaMemoryJob.Query().
		Where(kaguyamemoryjob.KindEQ(kaguyamemoryjob.KindBackfill),
			kaguyamemoryjob.ScopeKeyEQ(req.ScopeKey),
			kaguyamemoryjob.ConversationIDEQ(req.ConversationID),
			kaguyamemoryjob.StatusIn(kaguyamemoryjob.StatusPending, kaguyamemoryjob.StatusRunning,
				kaguyamemoryjob.StatusRetryWait, kaguyamemoryjob.StatusBlocked)).
		Order(ent.Desc(kaguyamemoryjob.FieldCreatedAt)).First(ctx)
	if err == nil {
		return s.backfillProgress(existing)
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	state := backfillState{Config: backfillConfig{
		ScopeKey: req.ScopeKey, ConversationID: req.ConversationID,
		After: req.After, Before: req.Before, MaxSources: req.MaxSources,
	}}
	payload, err := encodeBackfillState(state)
	if err != nil {
		return nil, err
	}
	job, err := s.client.KaguyaMemoryJob.Create().
		SetKind(kaguyamemoryjob.KindBackfill).
		SetScopeKey(req.ScopeKey).SetConversationID(req.ConversationID).
		SetStatus(kaguyamemoryjob.StatusPending).
		SetCompilerVersion(CompilerVersion).
		SetPolicyEpoch(policy.Epoch).
		SetResultJSON(payload).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	Notify()
	return s.backfillProgress(job)
}

// backfillProgress 把作业状态转换为进度响应。
func (s *Service) backfillProgress(job *ent.KaguyaMemoryJob) (*dtomemory.MemoryBackfillResp, error) {
	resp := &dtomemory.MemoryBackfillResp{
		JobID: job.ID, ScopeKey: job.ScopeKey, Status: string(job.Status),
		ErrorCode: job.ErrorCode,
	}
	state, err := decodeBackfillState(job)
	if err != nil {
		return resp, nil
	}
	resp.Scanned, resp.Created, resp.Skipped = state.Scanned, state.Created, state.Skipped
	resp.MaxSources, resp.Limited, resp.Finished = state.Config.MaxSources, state.Limited, state.Finished
	return resp, nil
}

// conversationInScope 校验会话属于给定范围且未删除；不接受请求覆盖归属。
func (s *Service) conversationInScope(ctx context.Context, scope, conversationID string) (string, error) {
	row, err := s.client.KaguyaConversation.Query().
		Where(kaguyaconversation.IDEQ(conversationID), kaguyaconversation.DeletedAtIsNil()).
		Select(kaguyaconversation.FieldProjectID).Only(ctx)
	if ent.IsNotFound(err) {
		return "", fmt.Errorf("%w: conversation not found", ErrPlanInvalid)
	}
	if err != nil {
		return "", err
	}
	projectID := ""
	if row.ProjectID != nil {
		projectID = *row.ProjectID
	}
	if ScopeKey(projectID) != scope {
		return "", fmt.Errorf("%w: conversation does not belong to scope %q", ErrPlanInvalid, scope)
	}
	return projectID, nil
}

// claimBackfillJob 领取一个到期的回填作业；回填不调用模型，不产生 Attempt。
func (w *Worker) claimBackfillJob(ctx context.Context) *ent.KaguyaMemoryJob {
	now := nowTime()
	job, err := w.svc.client.KaguyaMemoryJob.Query().
		Where(kaguyamemoryjob.KindEQ(kaguyamemoryjob.KindBackfill),
			kaguyamemoryjob.Or(
				kaguyamemoryjob.StatusEQ(kaguyamemoryjob.StatusPending),
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
		w.svc.logger.Warn("query backfill job failed", zap.Error(err))
		return nil
	}
	claimed, err := w.svc.claimBackfillTx(ctx, job)
	if err != nil {
		if !errors.Is(err, ErrStaleLease) {
			w.svc.logger.Warn("claim backfill job failed", zap.Error(err))
		}
		return nil
	}
	return claimed
}

// claimBackfillTx 以新 lease token 领取回填作业；并发领取只允许一个成功。
func (s *Service) claimBackfillTx(ctx context.Context, job *ent.KaguyaMemoryJob) (*ent.KaguyaMemoryJob, error) {
	lease := newLeaseToken()
	updated, err := s.client.KaguyaMemoryJob.UpdateOneID(job.ID).
		Where(kaguyamemoryjob.StatusIn(kaguyamemoryjob.StatusPending, kaguyamemoryjob.StatusRetryWait,
			kaguyamemoryjob.StatusRunning),
			kaguyamemoryjob.LeaseTokenEQ(job.LeaseToken)).
		SetStatus(kaguyamemoryjob.StatusRunning).
		SetAttempt(job.Attempt + 1).
		SetLeaseToken(lease).SetLeaseExpiresAt(nowTime().Add(leaseDuration)).
		SetStartedAt(nowTime()).Save(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrStaleLease
	}
	if err != nil {
		return nil, err
	}
	updated.LeaseToken = lease
	return updated, nil
}

// backfillWriteContext 为终态写入派生一个短时独立 context：进程关停或 SSE 取消后
// 仍要能保存作业状态，而不是把作业留在 running 等下次回收。
func backfillWriteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil || ctx.Err() != nil {
		return context.WithTimeout(context.Background(), 5*time.Second)
	}
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

// runBackfill 处理一页回填：读取轮次 → 幂等写入来源 → 保存游标与进度。
// 失败时游标不推进，重跑会跳过已存在来源；部分成功不会从头再来。
func (s *Service) runBackfill(ctx context.Context, job *ent.KaguyaMemoryJob) {
	if ctx.Err() != nil {
		s.retryBackfill(ctx, job, "canceled", nil)
		return
	}
	state, err := decodeBackfillState(job)
	if err != nil {
		s.finishBackfill(ctx, job, state, "invalid_state", err)
		return
	}
	policy, err := LoadPolicy(ctx, s.client)
	if err != nil {
		s.retryBackfill(ctx, job, "load_policy", err)
		return
	}
	if !policy.Enabled {
		s.blockBackfill(ctx, job, "config", "memory disabled")
		return
	}
	if err := ValidateScope(ctx, s.client, job.ScopeKey); err != nil {
		s.finishBackfill(ctx, job, state, "scope_invalid", err)
		return
	}
	turns, err := s.backfillTurnPage(ctx, job, state)
	if err != nil {
		s.retryBackfill(ctx, job, "query_turns", err)
		return
	}
	// 已处理数据过滤：先查出本页已存在的来源键，再只写缺失部分。
	keys := make([]string, 0, len(turns))
	for _, turn := range turns {
		keys = append(keys, backfillSourceKey(turn.ID))
	}
	existing := map[string]bool{}
	if len(keys) > 0 {
		rows, err := s.client.KaguyaMemorySource.Query().
			Where(kaguyamemorysource.SourceKeyIn(keys...)).
			Select(kaguyamemorysource.FieldSourceKey).All(ctx)
		if err != nil {
			s.retryBackfill(ctx, job, "query_sources", err)
			return
		}
		for _, row := range rows {
			existing[row.SourceKey] = true
		}
	}
	// 成本上限只截断新增来源；已扫描的轮次推进游标，重跑不会重复扫描。
	newTurns := make([]*ent.KaguyaChatTurn, 0, len(turns))
	skipped := 0
	limited := false
	processed := 0
	for _, turn := range turns {
		if existing[backfillSourceKey(turn.ID)] {
			skipped++
			processed++
			continue
		}
		if state.Created+len(newTurns) >= state.Config.MaxSources {
			limited = true
			break
		}
		newTurns = append(newTurns, turn)
		processed++
	}
	updated, err := s.commitBackfillPage(ctx, job, turns[:processed], newTurns, state, skipped, limited)
	if err != nil {
		s.retryBackfill(ctx, job, "commit_page", err)
		return
	}
	done := limited || len(turns) < backfillPageSize
	if done {
		updated.Finished = true
		s.finishBackfillState(ctx, job, updated, "")
		return
	}
	// 还有下一页：短延迟后由 Worker 继续领取，不阻塞其他作业。
	if err := s.client.KaguyaMemoryJob.UpdateOneID(job.ID).
		Where(kaguyamemoryjob.LeaseTokenEQ(job.LeaseToken)).
		SetStatus(kaguyamemoryjob.StatusRetryWait).SetNextAttemptAt(nowTime()).
		SetLeaseToken("").ClearLeaseExpiresAt().Exec(ctx); err != nil {
		s.svcLogWarn("schedule next backfill page failed", err)
	}
	Notify()
}

// commitBackfillPage 在单个事务内写入本页新来源并推进游标与进度：
// 来源与 checkpoint 原子提交，崩溃后重跑只会跳过已存在来源。
func (s *Service) commitBackfillPage(ctx context.Context, job *ent.KaguyaMemoryJob,
	processed, newTurns []*ent.KaguyaChatTurn, state backfillState, skipped int, limited bool) (backfillState, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return state, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	for _, turn := range newTurns {
		projectID := ""
		if turn.Edges.Conversation != nil && turn.Edges.Conversation.ProjectID != nil {
			projectID = *turn.Edges.Conversation.ProjectID
		}
		if err := client.KaguyaMemorySource.Create().
			SetSourceKey(backfillSourceKey(turn.ID)).
			SetKind(kaguyamemorysource.KindTurn).
			SetScopeKey(ScopeKey(projectID)).
			SetConversationID(turn.ConversationID).
			SetTurnID(turn.ID).
			SetProjectionVersion(ProjectionVersion).
			SetState(kaguyamemorysource.StatePending).
			SetCapturedAt(nowTime()).
			SetPolicyEpoch(job.PolicyEpoch).
			OnConflictColumns(kaguyamemorysource.FieldSourceKey).
			Ignore().
			Exec(ctx); err != nil {
			return state, err
		}
	}
	state.Scanned += len(processed)
	state.Created += len(newTurns)
	state.Skipped += skipped
	state.Limited = state.Limited || limited
	if len(processed) > 0 {
		state.Cursor = processed[len(processed)-1].ID
	}
	payload, err := encodeBackfillState(state)
	if err != nil {
		return state, err
	}
	if err := client.KaguyaMemoryJob.UpdateOneID(job.ID).
		Where(kaguyamemoryjob.LeaseTokenEQ(job.LeaseToken)).
		SetResultJSON(payload).Exec(ctx); err != nil {
		return state, err
	}
	if err := tx.Commit(); err != nil {
		return state, err
	}
	return state, nil
}

// backfillTurnPage 按范围、时间与游标读取一页 completed 轮次；
// 私密/只读会话与已删除会话不参与回填。
func (s *Service) backfillTurnPage(ctx context.Context, job *ent.KaguyaMemoryJob, state backfillState) ([]*ent.KaguyaChatTurn, error) {
	query := s.client.KaguyaChatTurn.Query().
		Where(kaguyachatturn.StatusEQ(kaguyachatturn.StatusCompleted),
			kaguyachatturn.HasConversationWith(
				kaguyaconversation.DeletedAtIsNil(),
				kaguyaconversation.MemoryModeEQ(kaguyaconversation.MemoryModeInherit),
			)).
		WithConversation(func(q *ent.KaguyaConversationQuery) {
			q.Select(kaguyaconversation.FieldID, kaguyaconversation.FieldProjectID)
		})
	switch {
	case job.ScopeKey == ScopePersonal:
		query.Where(kaguyachatturn.HasConversationWith(kaguyaconversation.ProjectIDIsNil()))
	case strings.HasPrefix(job.ScopeKey, scopeProjectPrefix):
		projectID := strings.TrimPrefix(job.ScopeKey, scopeProjectPrefix)
		query.Where(kaguyachatturn.HasConversationWith(kaguyaconversation.ProjectIDEQ(projectID)))
	default:
		return nil, fmt.Errorf("%w: unsupported backfill scope", ErrPlanInvalid)
	}
	if state.Config.ConversationID != "" {
		query.Where(kaguyachatturn.ConversationIDEQ(state.Config.ConversationID))
	}
	if state.Config.After != nil {
		query.Where(kaguyachatturn.FinishedAtGTE(*state.Config.After))
	}
	if state.Config.Before != nil {
		query.Where(kaguyachatturn.FinishedAtLTE(*state.Config.Before))
	}
	if state.Cursor != "" {
		query.Where(kaguyachatturn.IDGT(state.Cursor))
	}
	return query.Order(ent.Asc(kaguyachatturn.FieldID)).Limit(backfillPageSize).All(ctx)
}

// backfillSourceKey 与自动捕获共用同一稳定键：回填与在线捕获互为幂等。
func backfillSourceKey(turnID string) string {
	return fmt.Sprintf("turn:%s:projection-v%d", turnID, ProjectionVersion)
}

// finishBackfill 记录终态失败；错误摘要不包含源文本。
func (s *Service) finishBackfill(ctx context.Context, job *ent.KaguyaMemoryJob, state backfillState, code string, cause error) {
	state.Finished = true
	payload, err := encodeBackfillState(state)
	if err != nil {
		s.svcLogWarn("encode backfill state failed", err)
		return
	}
	writeCtx, cancel := backfillWriteContext(ctx)
	defer cancel()
	if err := s.client.KaguyaMemoryJob.UpdateOneID(job.ID).
		Where(kaguyamemoryjob.LeaseTokenEQ(job.LeaseToken)).
		SetStatus(kaguyamemoryjob.StatusFailed).
		SetResultJSON(payload).
		SetLeaseToken("").ClearLeaseExpiresAt().
		SetErrorCode(code).SetErrorSummary(code).
		SetFinishedAt(nowTime()).Exec(writeCtx); err != nil {
		s.svcLogWarn("finish backfill failed", err)
	}
}

// finishBackfillState 成功终态；limited 表示达到用户设置的成本上限。
func (s *Service) finishBackfillState(ctx context.Context, job *ent.KaguyaMemoryJob, state backfillState, code string) {
	payload, err := encodeBackfillState(state)
	if err != nil {
		s.svcLogWarn("encode backfill state failed", err)
		return
	}
	writeCtx, cancel := backfillWriteContext(ctx)
	defer cancel()
	if err := s.client.KaguyaMemoryJob.UpdateOneID(job.ID).
		Where(kaguyamemoryjob.LeaseTokenEQ(job.LeaseToken)).
		SetStatus(kaguyamemoryjob.StatusSucceeded).
		SetResultJSON(payload).
		SetLeaseToken("").ClearLeaseExpiresAt().
		SetErrorCode(code).SetFinishedAt(nowTime()).Exec(writeCtx); err != nil {
		s.svcLogWarn("finish backfill failed", err)
	}
}

// retryBackfill 短暂失败退回重试；游标保持在上一成功分页，不会重头执行。
func (s *Service) retryBackfill(ctx context.Context, job *ent.KaguyaMemoryJob, code string, cause error) {
	if cause != nil {
		s.svcLogWarn("backfill step failed", cause)
	}
	delay := baseRetryDelay
	summary := code
	writeCtx, cancel := backfillWriteContext(ctx)
	defer cancel()
	if err := s.client.KaguyaMemoryJob.UpdateOneID(job.ID).
		Where(kaguyamemoryjob.LeaseTokenEQ(job.LeaseToken)).
		SetStatus(kaguyamemoryjob.StatusRetryWait).
		SetNextAttemptAt(nowTime().Add(delay)).
		SetLeaseToken("").ClearLeaseExpiresAt().
		SetErrorCode(code).SetErrorSummary(summary).Exec(writeCtx); err != nil {
		s.svcLogWarn("schedule backfill retry failed", err)
	}
}

// blockBackfill 等待明确变化（记忆总开关重新开启）；不无限忙重试。
func (s *Service) blockBackfill(ctx context.Context, job *ent.KaguyaMemoryJob, code, summary string) {
	writeCtx, cancel := backfillWriteContext(ctx)
	defer cancel()
	if err := s.client.KaguyaMemoryJob.UpdateOneID(job.ID).
		Where(kaguyamemoryjob.LeaseTokenEQ(job.LeaseToken)).
		SetStatus(kaguyamemoryjob.StatusBlocked).
		SetLeaseToken("").ClearLeaseExpiresAt().
		SetNextAttemptAt(nowTime().Add(24 * time.Hour)).
		SetErrorCode(code).SetErrorSummary(summary).Exec(writeCtx); err != nil {
		s.svcLogWarn("block backfill failed", err)
	}
}

// backfillJobProgress 供任务列表展示回填进度。
func backfillJobProgress(job *ent.KaguyaMemoryJob) *dtomemory.MemoryJobProgress {
	state, err := decodeBackfillState(job)
	if err != nil {
		return nil
	}
	return &dtomemory.MemoryJobProgress{
		Scanned: state.Scanned, Created: state.Created, Skipped: state.Skipped,
		Limited: state.Limited, Finished: state.Finished,
	}
}
