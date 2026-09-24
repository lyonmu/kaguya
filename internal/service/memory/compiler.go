package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"charm.land/fantasy"
	agentruntime "github.com/lyonmu/kaguya/internal/agent/runtime"
	token "github.com/lyonmu/kaguya/internal/agent/token"
	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryattempt"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
	"go.uber.org/zap"
)

// 单次编译调用的响应与输出预算；maxOutput 取批次预算、模型输出上限和
// 保守窗口余量的交集。byte/4 估算不是 tokenizer，中文场景按保守余量处理。
const (
	maxResultBytes     = 64 << 10
	maxPlanOutputChars = 24000
)

// CallResult 是一次任务模型调用的结果与用量。
// 生成错误路径未返回上游用量时 UsageKnown=false，不能按 0 计入确认消耗。
type CallResult struct {
	Text         string
	Usage        token.NormalizedUsage
	UsageKnown   bool
	FinishReason string
}

// ModelCaller 抽象任务模型调用，便于测试注入；真实实现走现有 runtime，
// 不传工具、不追加 MCP、不使用聊天历史。
type ModelCaller interface {
	Call(ctx context.Context, cfg agentruntime.ProviderConfig, systemPrompt, payload string, maxOutput int64) (CallResult, error)
}

// RuntimeCaller 用现有 Fantasy runtime 执行一次严格 JSON 生成。
// 模型调用内部 MaxRetries=0，重试由 Job 层统一控制，避免重试次数相乘。
type RuntimeCaller struct{}

func (RuntimeCaller) Call(ctx context.Context, cfg agentruntime.ProviderConfig, systemPrompt, payload string, maxOutput int64) (CallResult, error) {
	ag, err := agentruntime.New(
		agentruntime.WithProvider(cfg),
		agentruntime.WithSystemPrompt(systemPrompt),
	)
	if err != nil {
		return CallResult{}, err
	}
	retries := 0
	result, err := ag.Generate(ctx, fantasy.AgentCall{
		Prompt:          payload,
		MaxOutputTokens: &maxOutput,
		MaxRetries:      &retries,
	})
	if err != nil {
		return CallResult{}, err
	}
	if result == nil {
		return CallResult{}, errors.New("empty memory result")
	}
	call := CallResult{
		Usage:        token.FromFantasyUsage(result.TotalUsage),
		UsageKnown:   true,
		FinishReason: string(result.Response.FinishReason),
	}
	if result.Response.FinishReason != fantasy.FinishReasonStop {
		return call, fmt.Errorf("memory generation did not finish normally (finish reason: %s)", result.Response.FinishReason)
	}
	raw := result.Response.Content.Text()
	if len(raw) > maxResultBytes {
		return call, errors.New("memory result exceeds size limit")
	}
	call.Text = raw
	return call, nil
}

// contractError 携带未通过契约校验的响应原文，供一次有界修复调用使用；
// 原文绝不写入日志。
type contractError struct {
	raw string
	err error
}

func (e *contractError) Error() string { return e.err.Error() }
func (e *contractError) Unwrap() error { return e.err }

// isContractError 判断失败是否为响应契约问题（可做一次有界修复）。
func isContractError(err error) bool {
	var target *contractError
	return errors.As(err, &target)
}

// strictDecodeJSON 严格解析模型响应：拒绝未知字段与尾随 JSON。
// 不把原始 invalid JSON 写进日志。
func strictDecodeJSON(raw string, out any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return &contractError{raw: raw, err: fmt.Errorf("invalid memory JSON contract: %w", err)}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return &contractError{raw: raw, err: errors.New("unexpected trailing memory output")}
	}
	return nil
}

// callTracked 执行一次模型调用并把用量/结果码记入 Attempt。
// 所有分支（含错误与修复调用）都会落一条 Attempt，失败重试同样可能收费。
func (s *Service) callTracked(ctx context.Context, job *ent.KaguyaMemoryJob, phase kaguyamemoryattempt.Phase, target *servicesystem.TaskModel, systemPrompt, payload string) (CallResult, error) {
	maxOutput := callOutputLimit(target, len(payload))
	startedAt := time.Now()
	result, err := s.caller.Call(ctx, target.Config, systemPrompt, payload, maxOutput)
	duration := time.Since(startedAt)
	resultCode := "ok"
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		resultCode = "canceled"
	default:
		resultCode, _ = classifyCallError(err)
	}
	attempt := job.Attempt
	if attempt < 1 {
		attempt = 1
	}
	fields := func(set func(*ent.KaguyaMemoryAttemptCreate)) {}
	_ = fields
	// 领取时已登记本尝试的首个 Attempt 行（result=started）：调用完成后补全用量；
	// 修复调用与后续阶段各自新增一行，失败重试同样可能收费。
	pending, queryErr := s.client.KaguyaMemoryAttempt.Query().
		Where(kaguyamemoryattempt.JobIDEQ(job.ID), kaguyamemoryattempt.AttemptEQ(attempt),
			kaguyamemoryattempt.PhaseEQ(phase), kaguyamemoryattempt.ResultCodeEQ("started")).
		First(ctx)
	if queryErr == nil {
		if saveErr := s.client.KaguyaMemoryAttempt.UpdateOneID(pending.ID).
			SetModelRecordID(target.ModelRecordID).SetProviderID(target.ProviderID).SetUpstreamModelID(target.UpstreamModelID).
			SetUsageKnown(result.UsageKnown).
			SetInputTokens(result.Usage.InputTokens).SetOutputTokens(result.Usage.OutputTokens).
			SetTotalTokens(result.Usage.TotalTokens).SetCachedTokens(result.Usage.CacheHitTokens).
			SetReasoningTokens(result.Usage.ReasoningTokens).
			SetDurationMs(max(duration.Milliseconds(), 0)).SetResultCode(resultCode).Exec(ctx); saveErr != nil {
			s.logger.Warn("record memory attempt failed", zap.Error(saveErr))
		}
		return result, err
	}
	create := s.client.KaguyaMemoryAttempt.Create().
		SetJobID(job.ID).SetAttempt(attempt).SetPhase(phase).
		SetModelRecordID(target.ModelRecordID).SetProviderID(target.ProviderID).SetUpstreamModelID(target.UpstreamModelID).
		SetUsageKnown(result.UsageKnown).
		SetInputTokens(result.Usage.InputTokens).SetOutputTokens(result.Usage.OutputTokens).
		SetTotalTokens(result.Usage.TotalTokens).SetCachedTokens(result.Usage.CacheHitTokens).
		SetReasoningTokens(result.Usage.ReasoningTokens).
		SetDurationMs(max(duration.Milliseconds(), 0)).SetResultCode(resultCode)
	if saveErr := create.Exec(ctx); saveErr != nil {
		s.logger.Warn("record memory attempt failed", zap.Error(saveErr))
	}
	return result, err
}

// callOutputLimit 计算一次调用的输出上限：批次预算、模型输出上限与
// 保守窗口余量的交集；窗口未知时按小型固定上限。
func callOutputLimit(target *servicesystem.TaskModel, payloadBytes int) int64 {
	limit := int64(maxPlanOutputChars / 4)
	if target.TokenMaxOutputTokens > 0 {
		limit = min(limit, int64(target.TokenMaxOutputTokens))
	}
	if target.TokenContextWindow > 0 {
		// 输入按 byte/4 保守估算，窗口的 3/4 可用于输出与协议开销。
		room := int64(target.TokenContextWindow)*3/4 - int64(payloadBytes)/4
		limit = min(limit, max(room, 1))
	}
	return max(limit, 1)
}

// classifyCallError 把上游错误映射为安全错误码与重试策略：
// 认证失败与输入预算问题直接 blocked，429/5xx/网络问题指数退避重试，
// 尊重可用的 Retry-After。
func classifyCallError(err error) (code string, retryAfter time.Duration) {
	var providerErr *fantasy.ProviderError
	if !errors.As(err, &providerErr) {
		return "provider_error", 0
	}
	switch {
	case providerErr.IsContextTooLarge() || providerErr.ContextTooLargeErr:
		return "input_budget", 0
	case providerErr.StatusCode == 401 || providerErr.StatusCode == 403:
		return "auth", 0
	case providerErr.StatusCode == 429:
		return "rate_limited", retryAfterSeconds(providerErr.ResponseHeaders)
	case providerErr.StatusCode == 408 || providerErr.StatusCode >= 500 || providerErr.IsRetryable():
		return "provider_error", 0
	case providerErr.StatusCode >= 400:
		return "invalid_request", 0
	default:
		return "provider_error", 0
	}
}

// callRetryable 决定错误是否值得重试；认证、预算与明确的请求错误不重试。
func callRetryable(err error) bool {
	code, _ := classifyCallError(err)
	switch code {
	case "auth", "input_budget", "invalid_request":
		return false
	default:
		return true
	}
}

// callBlocked 决定错误是否应转为 blocked 等待用户修复。
func callBlocked(err error) bool {
	code, _ := classifyCallError(err)
	return code == "auth" || code == "input_budget"
}

func retryAfterSeconds(headers map[string]string) time.Duration {
	for key, value := range headers {
		if !strings.EqualFold(key, "retry-after") {
			continue
		}
		if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
		if at, err := httpParseTime(strings.TrimSpace(value)); err == nil {
			if wait := time.Until(at); wait > 0 {
				return wait
			}
		}
	}
	return 0
}

func httpParseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC1123, value)
}

// Extract 是阶段 A：从有界来源投影提炼小量 typed claims，不写页面。
func (s *Service) Extract(ctx context.Context, job *ent.KaguyaMemoryJob, target *servicesystem.TaskModel, payload []byte) (*dtomemory.ExtractResult, error) {
	result, err := s.callTracked(ctx, job, kaguyamemoryattempt.PhaseExtract, target, extractSystemPrompt, string(payload))
	if err != nil {
		return nil, err
	}
	var extracted dtomemory.ExtractResult
	if err := strictDecodeJSON(result.Text, &extracted); err != nil {
		return nil, err
	}
	return &extracted, nil
}

// Repair 是阶段 A/C 的一次有界格式修复调用；总调用次数仍受任务预算控制。
func (s *Service) Repair(ctx context.Context, job *ent.KaguyaMemoryJob, target *servicesystem.TaskModel, broken string) (string, error) {
	payload := "The previous response did not match the required JSON contract. Return only the corrected JSON.\nPrevious response:\n" + truncateRunes(broken, 8000)
	result, err := s.callTracked(ctx, job, kaguyamemoryattempt.PhaseRepair, target, extractSystemPrompt, payload)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// Plan 是阶段 C：检索候选后生成 PatchPlan，不执行任意工具。
func (s *Service) Plan(ctx context.Context, job *ent.KaguyaMemoryJob, target *servicesystem.TaskModel, payload []byte) (*dtomemory.PatchPlan, error) {
	result, err := s.callTracked(ctx, job, kaguyamemoryattempt.PhasePlan, target, planSystemPrompt, string(payload))
	if err != nil {
		return nil, err
	}
	var plan dtomemory.PatchPlan
	if err := strictDecodeJSON(result.Text, &plan); err != nil {
		return nil, err
	}
	return &plan, nil
}
