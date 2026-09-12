package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"charm.land/fantasy"
	agentruntime "github.com/lyonmu/kaguya/internal/agent/runtime"
	token "github.com/lyonmu/kaguya/internal/agent/token"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamodelsinfo"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaproviderinfo"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/internal/secret"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
)

// chatTarget 是本次对话使用的系统配置、模型记录及其提供商。
type chatTarget struct {
	info     *dtosystem.SystemInfoResp
	model    *ent.KaguyaModelsInfo
	provider *ent.KaguyaProviderInfo
}

// chatExecution 是一次轮次的不可变输入：目标配置、会话身份与已组装的提示词。
// trace/turnID 指向已创建的 running 占位行，供增量落库与提交使用。
type chatExecution struct {
	target             *chatTarget
	conversationID     string
	version            int64
	history            []fantasy.Message
	prompt             *chatPrompt
	requestedProjectID string
	userContent        string
	trace              *turnTrace
	turnID             string
}

// resolveChatTarget 读取系统配置并解析本次对话的模型与提供商。
// 使用本地模型记录 ID 查询，避免不同提供商出现相同上游模型名时冲突。
func resolveChatTarget(ctx context.Context, requestedModelID string) (*chatTarget, error) {
	info, err := (&servicesystem.SystemSvc{}).Info(ctx)
	if err != nil {
		return nil, err
	}
	modelID := requestedModelID
	if modelID == "" {
		modelID = info.DefaultModelID
	}
	if modelID == "" {
		return nil, ErrChatModelNotConfigured
	}
	model, err := db.EntClient.KaguyaModelsInfo.Query().
		Where(kaguyamodelsinfo.DeletedAtIsNil(), kaguyamodelsinfo.HasProviderWith(kaguyaproviderinfo.DeletedAtIsNil())).
		WithProvider().
		Where(kaguyamodelsinfo.IDEQ(modelID)).
		First(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("query chat model failed, err is %+v", err)
		return nil, err
	}
	provider := model.Edges.Provider
	if provider == nil {
		err = fmt.Errorf("chat model %q has no provider", model.ModelID)
		global.Logger.Sugar().Error(err)
		return nil, err
	}
	return &chatTarget{info: info, model: model, provider: provider}, nil
}

// newConversationID 为新会话生成雪花 ID；请求已带 ID 时原样沿用。
func newConversationID(requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	id, err := global.Id.GenID()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", id), nil
}

// ErrProviderSecretUnavailable 表示已存储的 API Key 无法用当前密钥解密。
// 常见原因是 TLS 证书被外部替换而数据库未同步；API 层据此提示重新填写。
var ErrProviderSecretUnavailable = errors.New("provider API key cannot be decrypted")

// providerAPIKey 解密提供商的 API Key。密文无法解开时统一返回
// ErrProviderSecretUnavailable，避免前端只看到通用的对话失败。
func providerAPIKey(provider *ent.KaguyaProviderInfo, conversationID string) (string, error) {
	apiKey, err := secret.Decrypt(provider.APIKey)
	if err != nil {
		global.Logger.Sugar().Errorf("decrypt provider api key failed: conversation_id=%s provider_id=%s err=%v", conversationID, provider.ID, err)
		return "", ErrProviderSecretUnavailable
	}
	return apiKey, nil
}

// buildChatAgent 组装本次请求的 Agent（每次请求新建）。
// API Key 以密文存储，只在这里解密后交给运行时。
func buildChatAgent(exec chatExecution) (*agentruntime.Agent, error) {
	provider := exec.target.provider
	apiKey, err := providerAPIKey(provider, exec.conversationID)
	if err != nil {
		return nil, err
	}
	return agentruntime.New(
		agentruntime.WithProvider(agentruntime.ProviderConfig{
			Name: provider.ProviderName, Type: provider.ProviderType, Protocol: consts.ProviderProtocol(provider.APIProtocol),
			BaseURL: provider.BaseURL, APIKey: apiKey, ModelID: exec.target.model.ModelID,
			ConversationID: exec.conversationID, UserAgent: exec.target.info.UserAgent,
		}),
		agentruntime.WithSystemPrompt(exec.prompt.system),
		agentruntime.WithTools(exec.prompt.tools...),
	)
}

// streamChat 执行一次流式生成，返回完整结果。
// Fantasy 对临时提供商错误执行指数退避；每个 step 单独判断，当前 step 尚未输出时可安全
// 重试，一旦输出过任何块便取消重试，避免正文重复。
func (s *AgentSvc) streamChat(ctx context.Context, dataChan chan *dtochat.ChatResp, agent *agentruntime.Agent, exec chatExecution) (*chatOutcome, error) {
	info, model, provider := exec.target.info, exec.target.model, exec.target.provider

	streamCtx := token.WithConversationID(ctx, exec.conversationID)
	retryCtx, cancelRetries := context.WithCancel(streamCtx)
	defer cancelRetries()

	var stepStreamed atomic.Bool
	stream := newChatStream(func(block dtochat.ContentBlock) error {
		stepStreamed.Store(true)
		frame := dtochat.Chat{ID: exec.conversationID, Flag: dtochat.WSFlagDelta, Block: &block}
		if block.Type == dtochat.BlockTypeText && block.Phase == dtochat.BlockPhaseDelta {
			frame.Content = block.Text
		}
		if !send(ctx, dataChan, &dtochat.ChatResp{
			Chat:        frame,
			APIProtocol: consts.ProviderProtocol(provider.APIProtocol),
			Created:     time.Now().Unix(), ModelID: model.ModelID, ModelName: model.ModelName,
		}) {
			return ctx.Err()
		}
		return nil
	})
	call := stream.callbacks()

	var retryPreventedError atomic.Pointer[fantasy.ProviderError]
	retryCount := 0
	call.OnStepStart = func(_ int) error {
		stepStreamed.Store(false)
		return nil
	}
	call.OnRetry = func(err *fantasy.ProviderError, delay time.Duration) {
		if stepStreamed.Load() {
			retryPreventedError.CompareAndSwap(nil, err)
			cancelRetries()
			return
		}
		retryCount++
		global.Logger.Sugar().Warnf("retry chat stream: conversation_id=%s retry=%d/%d delay=%s err=%v", exec.conversationID, retryCount, *info.ChatMaxRetries, delay, err)
	}

	maxRetries := *info.ChatMaxRetries
	call.MaxRetries = &maxRetries
	call.MaxOutputTokens = contextOutputLimit(model.TokenContextWindow, model.TokenMaxOutputTokens, *info.ContextCompactionPercent)
	if len(exec.prompt.tools) > 0 && *info.AgentMaxSteps > 0 {
		call.StopWhen = []fantasy.StopCondition{fantasy.StepCountIs(*info.AgentMaxSteps)}
	}

	compactor, err := s.newContextCompactor(ctx, exec, info)
	if err != nil {
		return nil, err
	}
	call.PrepareStep = compactor.prepare

	trace := exec.trace
	if trace == nil {
		trace = newTurnTrace()
	}
	trace.wrap(&call)
	call.Prompt = exec.prompt.requestPrompt
	call.Messages = exec.history

	startedAt := time.Now()
	result, err := agent.Stream(retryCtx, call)
	finishedAt := time.Now()
	if retryErr := retryPreventedError.Load(); retryErr != nil {
		err = retryErr
	}
	if err == nil {
		err = ctx.Err()
	}
	// 截断、过滤、未知终止也不算完整结束，不保存部分上下文。
	if err == nil && result == nil {
		err = fmt.Errorf("conversation returned no result")
	}
	if err != nil {
		return nil, err
	}

	paused := *info.AgentMaxSteps > 0 && len(result.Steps) >= *info.AgentMaxSteps && result.Steps[len(result.Steps)-1].FinishReason == fantasy.FinishReasonToolCalls
	if !paused && result.Response.FinishReason != fantasy.FinishReasonStop {
		return nil, fmt.Errorf("conversation did not finish normally (finish reason: %s, step limit: %d); tool side effects may already have occurred", result.Response.FinishReason, *info.AgentMaxSteps)
	}
	// 有工具调用却没有结果说明轨迹不完整，不能作为完整轮次落库。
	if err := trace.finish(finishedAt); err != nil {
		global.Logger.Sugar().Errorf("incomplete conversation trace: id=%s err=%v", exec.conversationID, err)
		return nil, err
	}
	return &chatOutcome{
		result: result, trace: trace, compactor: compactor,
		startedAt: startedAt, finishedAt: finishedAt, paused: paused,
		finishReason: chatFinishReason(result, paused),
		usage:        chatTurnUsage(result, compactor),
	}, nil
}

// chatTurnUsage 合并生成用量与上下文压缩产生的额外用量。
func chatTurnUsage(result *fantasy.AgentResult, compactor *contextCompactor) token.NormalizedUsage {
	usage := token.FromFantasyUsage(result.TotalUsage)
	summary := token.FromFantasyUsage(compactor.usage)
	usage.InputTokens += summary.InputTokens
	usage.OutputTokens += summary.OutputTokens
	usage.TotalTokens += summary.TotalTokens
	usage.CacheHitTokens += summary.CacheHitTokens
	usage.ReasoningTokens += summary.ReasoningTokens
	return usage
}

// newContextCompactor 为本次轮次建立上下文压缩器，并把工具 Schema 的消息开销计入估算。
func (s *AgentSvc) newContextCompactor(ctx context.Context, exec chatExecution, info *dtosystem.SystemInfoResp) (*contextCompactor, error) {
	model := exec.target.model
	compactor := &contextCompactor{
		window: model.TokenContextWindow, percent: *info.ContextCompactionPercent,
		maxOutput: model.TokenMaxOutputTokens,
	}
	for _, item := range exec.prompt.tools {
		data, err := json.Marshal(item.Info())
		if err != nil {
			return nil, err
		}
		compactor.toolTokens += int64((len(data) + 3) / 4)
	}
	// 续聊时用上一次完成轮次的实际占用作为基线，避免重复估算整段历史。
	if exec.version > 0 {
		previous, err := s.ConversationContext(ctx, exec.conversationID)
		if err != nil {
			return nil, err
		}
		if previous.ModelID == model.ModelID && previous.ContextTokens != nil {
			estimate := *previous.ContextTokens + estimateMessages([]fantasy.Message{fantasy.NewUserMessage(exec.prompt.requestPrompt)})
			compactor.lastTokens = &estimate
		}
	}
	return compactor, nil
}
