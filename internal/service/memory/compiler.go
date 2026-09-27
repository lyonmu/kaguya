package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"charm.land/fantasy"
	agentruntime "github.com/lyonmu/kaguya/internal/agent/runtime"
	token "github.com/lyonmu/kaguya/internal/agent/token"
	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryattempt"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryjob"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
	"go.uber.org/zap"
)

var errInputBudget = errors.New("memory input exceeds budget")
var errDailyBudget = errors.New("daily memory budget exhausted")
var errOutputLimit = errors.New("task model reached its output token limit before completing memory JSON")

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
	var outputLimit *int64
	if maxOutput > 0 {
		outputLimit = &maxOutput
	}
	// 流式接收推理与正文，避免非流式长推理一直等不到响应头而在两分钟超时。
	result, err := ag.Stream(ctx, fantasy.AgentStreamCall{
		Prompt:          payload,
		MaxOutputTokens: outputLimit,
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
	if result.Response.FinishReason == fantasy.FinishReasonLength {
		return call, errOutputLimit
	}
	if result.Response.FinishReason != fantasy.FinishReasonStop {
		return call, fmt.Errorf("memory generation did not finish normally (finish reason: %s)", result.Response.FinishReason)
	}
	raw := result.Response.Content.Text()
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
	if err := s.checkJobAccess(ctx, job); err != nil {
		return CallResult{}, err
	}
	if err := checkDailyBudget(ctx, s.client); err != nil {
		return CallResult{}, err
	}
	if target.TokenContextWindow > 0 && (len(payload)+len(systemPrompt)+3)/4+256 >= target.TokenContextWindow*3/4 {
		return CallResult{}, errInputBudget
	}
	// 每次调用续租；慢速提炼不应耗尽后续整合阶段的执行时间。
	if err := s.client.KaguyaMemoryJob.UpdateOneID(job.ID).
		Where(kaguyamemoryjob.StatusEQ(kaguyamemoryjob.StatusRunning), kaguyamemoryjob.LeaseTokenEQ(job.LeaseToken)).
		SetLeaseExpiresAt(nowTime().Add(leaseDuration)).Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return CallResult{}, ErrStaleLease
		}
		return CallResult{}, err
	}
	ctx, cancelCall := context.WithTimeout(ctx, leaseDuration-30*time.Second)
	defer cancelCall()
	maxOutput := callOutputLimit(target, len(payload)+len(systemPrompt))
	startedAt := time.Now()
	result, err := s.caller.Call(ctx, target.Config, systemPrompt, payload, maxOutput)
	duration := time.Since(startedAt)
	resultCode := "ok"
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled):
		resultCode = "canceled"
	default:
		resultCode, _ = classifyCallError(err)
	}
	attempt := job.Attempt
	if attempt < 1 {
		attempt = 1
	}
	// SSE/应用取消后仍需保存已经发生的远程消费。
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	ctx = recordCtx
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

// callOutputLimit 使用用户配置的模型能力，包含推理 token，不设记忆专属输出上限。
// 两项均未知时返回 0，让提供商使用默认值；不能臆造一个 6000 token 上限。
func callOutputLimit(target *servicesystem.TaskModel, payloadBytes int) int64 {
	limit := int64(target.TokenMaxOutputTokens)
	if target.TokenContextWindow > 0 {
		room := max(int64(target.TokenContextWindow)*3/4-int64(payloadBytes)/4, 1)
		if limit <= 0 || room < limit {
			limit = room
		}
	}
	return max(limit, 0)
}

// classifyCallError 把上游错误映射为安全错误码与重试策略：
// 认证失败与输入预算问题直接 blocked，429/5xx/网络问题指数退避重试，
// 尊重可用的 Retry-After。
func classifyCallError(err error) (code string, retryAfter time.Duration) {
	if errors.Is(err, errOutputLimit) {
		return "output_limit", 0
	}
	if errors.Is(err, context.Canceled) {
		return "canceled", 0
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return "timeout", 0
	}
	if errors.Is(err, errInputBudget) {
		return "input_budget", 0
	}
	if errors.Is(err, errDailyBudget) {
		return "budget", 0
	}
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
	case "auth", "input_budget", "output_limit", "invalid_request":
		return false
	default:
		return true
	}
}

// callBlocked 决定错误是否应转为 blocked 等待用户修复。
func callBlocked(err error) bool {
	code, _ := classifyCallError(err)
	return code == "auth" || code == "input_budget" || code == "output_limit" || code == "budget"
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

// generateValidated 对格式和内容使用同一个校验入口，失败后最多修复一次。
// 修复保留原始资料与具体错误，否则模型无法纠正引用或旧页面版本。
func (s *Service) generateValidated(ctx context.Context, job *ent.KaguyaMemoryJob, target *servicesystem.TaskModel,
	phase kaguyamemoryattempt.Phase, prompt string, payload []byte, validate func(string) error) error {
	result, err := s.callTracked(ctx, job, phase, target, prompt, string(payload))
	if err != nil {
		return err
	}
	if err = validate(result.Text); err == nil {
		return nil
	}
	repair, marshalErr := json.Marshal(map[string]any{
		"input":             json.RawMessage(payload),
		"previous_response": result.Text,
		"validation_error":  err.Error(),
		"instruction":       "Correct the response using the original input. Return the complete JSON contract only. Do not invent evidence or omit useful candidates to bypass validation.",
	})
	if marshalErr != nil {
		return marshalErr
	}
	result, err = s.callTracked(ctx, job, kaguyamemoryattempt.PhaseRepair, target, prompt, string(repair))
	if err != nil {
		return err
	}
	if err := validate(result.Text); err != nil {
		return &contractError{raw: result.Text, err: err}
	}
	return nil
}

// Extract 提炼少量有来源的主张；显式空数组才表示没有长期知识。
func (s *Service) Extract(ctx context.Context, job *ent.KaguyaMemoryJob, target *servicesystem.TaskModel, payload []byte) (*dtomemory.ExtractResult, error) {
	var extracted dtomemory.ExtractResult
	err := s.generateValidated(ctx, job, target, kaguyamemoryattempt.PhaseExtract, extractSystemPrompt, payload, func(raw string) error {
		extracted = dtomemory.ExtractResult{}
		if err := strictDecodeJSON(raw, &extracted); err != nil {
			return err
		}
		if extracted.SchemaVersion != dtomemory.ContractSchemaVersion || extracted.Candidates == nil {
			return errors.New("schema_version must be 1 and candidates must be an array (use [] for no durable knowledge)")
		}
		return ValidateCandidates(extracted.Candidates, projectionsFromPayload(payload))
	})
	return &extracted, err
}

// Plan 生成完整知识页变更，修复后仍使用同一冻结候选与证据校验。
func (s *Service) Plan(ctx context.Context, job *ent.KaguyaMemoryJob, target *servicesystem.TaskModel, payload []byte, validate func(*dtomemory.PatchPlan) error) (*dtomemory.PatchPlan, error) {
	var plan dtomemory.PatchPlan
	err := s.generateValidated(ctx, job, target, kaguyamemoryattempt.PhasePlan, planSystemPrompt, payload, func(raw string) error {
		plan = dtomemory.PatchPlan{}
		if err := strictDecodeJSON(raw, &plan); err != nil {
			return err
		}
		if plan.SchemaVersion != dtomemory.ContractSchemaVersion || plan.Changes == nil {
			return errors.New("schema_version must be 1 and changes must be an array (use [] for no changes)")
		}
		return validate(&plan)
	})
	return &plan, err
}
