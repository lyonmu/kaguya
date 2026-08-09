// Package agent 组装并运行完整的 Agent 运行时（模型/提供商、system prompt、工具），
// 并在每次回答后记录运行信息（token 用量、耗时、思考时长、工具调用次数等）。
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"

	token "github.com/lyonmu/kaguya/internal/agent/token"
)

// 支持的提供商协议（与 internal/ent/schema 的 ProviderProtocol 保持一致）。
const (
	ProtocolOpenAI       = "openai"       // 兼容 OpenAI 的 API 协议
	ProtocolAnthropic    = "anthropic"    // Anthropic 原生 API 协议
	ProtocolOpenAICompat = "openaicompat" // OpenAI 兼容协议的第三方服务
)

// ProviderConfig 描述如何通过提供商协议构造底层模型。
type ProviderConfig struct {
	Name     string // 提供商名称（记录元数据用）
	Protocol string // 协议：openai / anthropic / openaicompat
	BaseURL  string // 可选，留空使用提供商官方默认地址
	APIKey   string // API Key
	ModelID  string // 调用 API 时使用的模型标识符
}

// Option 配置 Agent 的可选参数。
type Option func(*Agent)

// Agent 是 fantasy.Agent 的运行时包装：
// 1. 组装底层模型（WithProvider/WithModel）+ system prompt + 工具；
// 2. 包装 Generate/Stream，统计耗时，并在每次回答后调用 UsageRecorder 记录运行信息。
type Agent struct {
	inner    fantasy.Agent       // 组装好的底层 fantasy.Agent
	recorder token.UsageRecorder // 运行信息记录器，可为空
	logger   *slog.Logger        // 日志器，默认 slog.Default()

	provider string // 提供商名称（记录元数据）
	model    string // 模型名称（记录元数据）

	modelCfg    fantasy.LanguageModel // WithModel 注入的模型
	providerCfg ProviderConfig        // WithProvider 暂存的提供商配置
	modelCount  int                   // 模型来源设置次数（校验 0/1 个）

	systemPrompt string              // 系统提示词
	tools        []fantasy.AgentTool // 工具列表

	conversationIDFunc func(context.Context) string // 会话 ID 取值函数
	messageIDFunc      func(context.Context) string // 消息 ID 取值函数
}

// New 组装 Agent 运行时。必须且只能设置一次模型来源（WithProvider 或 WithModel）。
func New(opts ...Option) (*Agent, error) {
	a := &Agent{logger: slog.Default()}
	for _, opt := range opts {
		opt(a)
	}

	// 模型来源约束：有且只能有一个
	switch a.modelCount {
	case 0:
		return nil, errors.New("agent model is required: set WithModel or WithProvider")
	case 1:
	default:
		return nil, errors.New("agent model is configured more than once")
	}

	// 组装底层 fantasy.Agent，注入 system prompt 与工具
	var lm fantasy.LanguageModel
	if a.modelCfg != nil {
		lm = a.modelCfg
		a.provider = lm.Provider()
		a.model = lm.Model()
	} else {
		var err error
		lm, err = buildLanguageModel(context.Background(), a.providerCfg)
		if err != nil {
			return nil, err
		}
		a.provider = a.providerCfg.Name
		a.model = a.providerCfg.ModelID
	}

	agentOpts := make([]fantasy.AgentOption, 0, 2)
	if a.systemPrompt != "" {
		agentOpts = append(agentOpts, fantasy.WithSystemPrompt(a.systemPrompt))
	}
	if len(a.tools) > 0 {
		agentOpts = append(agentOpts, fantasy.WithTools(a.tools...))
	}
	a.inner = fantasy.NewAgent(lm, agentOpts...)

	// 默认从 context 读取会话/消息 ID，可用选项覆盖
	if a.conversationIDFunc == nil {
		a.conversationIDFunc = token.ConversationIDFromContext
	}
	if a.messageIDFunc == nil {
		a.messageIDFunc = token.MessageIDFromContext
	}

	return a, nil
}

// WithModel 直接注入底层模型（测试/自定义场景用）。与 WithProvider 二选一。
func WithModel(m fantasy.LanguageModel) Option {
	return func(a *Agent) {
		a.modelCount++
		a.modelCfg = m
	}
}

// WithProvider 按协议 + BaseURL + APIKey + ModelID 构造底层模型。与 WithModel 二选一。
// 模型在 New 阶段构造，构造失败（如未知协议）会由 New 返回错误。
func WithProvider(cfg ProviderConfig) Option {
	return func(a *Agent) {
		a.modelCount++
		a.providerCfg = cfg
	}
}

// WithSystemPrompt 设置系统提示词。
func WithSystemPrompt(prompt string) Option {
	return func(a *Agent) {
		a.systemPrompt = prompt
	}
}

// WithTools 注册 Agent 可用工具。
func WithTools(tools ...fantasy.AgentTool) Option {
	return func(a *Agent) {
		a.tools = append(a.tools, tools...)
	}
}

// WithRecorder 设置运行信息记录器（可为空，为空则不记录）。
func WithRecorder(r token.UsageRecorder) Option {
	return func(a *Agent) {
		a.recorder = r
	}
}

// WithLogger 设置日志器，默认 slog.Default()。记录失败时仅记 warning，不影响回答返回。
func WithLogger(l *slog.Logger) Option {
	return func(a *Agent) {
		if l != nil {
			a.logger = l
		}
	}
}

// WithConversationIDFunc 自定义会话 ID 取值函数，默认从 context 读取。
func WithConversationIDFunc(fn func(context.Context) string) Option {
	return func(a *Agent) {
		a.conversationIDFunc = fn
	}
}

// WithMessageIDFunc 自定义消息 ID 取值函数，默认从 context 读取。
func WithMessageIDFunc(fn func(context.Context) string) Option {
	return func(a *Agent) {
		a.messageIDFunc = fn
	}
}

// buildLanguageModel 按协议构造 fantasy.LanguageModel。
func buildLanguageModel(ctx context.Context, cfg ProviderConfig) (fantasy.LanguageModel, error) {
	var (
		provider fantasy.Provider
		err      error
	)

	switch cfg.Protocol {
	case ProtocolOpenAI:
		provider, err = openai.New(
			openai.WithAPIKey(cfg.APIKey),
			openai.WithBaseURL(cfg.BaseURL),
		)
	case ProtocolAnthropic:
		provider, err = anthropic.New(
			anthropic.WithAPIKey(cfg.APIKey),
			anthropic.WithBaseURL(cfg.BaseURL),
		)
	case ProtocolOpenAICompat:
		provider, err = openaicompat.New(
			openaicompat.WithAPIKey(cfg.APIKey),
			openaicompat.WithBaseURL(cfg.BaseURL),
		)
	default:
		return nil, fmt.Errorf("unsupported provider protocol %q", cfg.Protocol)
	}
	if err != nil {
		return nil, fmt.Errorf("create provider %q: %w", cfg.Protocol, err)
	}

	lm, err := provider.LanguageModel(ctx, cfg.ModelID)
	if err != nil {
		return nil, fmt.Errorf("create language model %q: %w", cfg.ModelID, err)
	}
	return lm, nil
}

// Generate 执行一次完整回答并记录运行信息。
func (a *Agent) Generate(ctx context.Context, call fantasy.AgentCall) (*fantasy.AgentResult, error) {
	startedAt := time.Now()
	result, err := a.inner.Generate(ctx, call)
	a.record(ctx, "generate", startedAt, nil, result, err)
	return result, err
}

// Stream 流式执行一次完整回答并记录运行信息。
// 与 Generate 不同，Stream 会包装 OnReasoningStart/OnReasoningEnd 回调统计思考时长，
// 用户传入的回调会被链式调用，不受影响。
func (a *Agent) Stream(ctx context.Context, call fantasy.AgentStreamCall) (*fantasy.AgentResult, error) {
	startedAt := time.Now()
	timer := a.wrapReasoningTimer(&call)
	result, err := a.inner.Stream(ctx, call)
	a.record(ctx, "stream", startedAt, timer, result, err)
	return result, err
}

// reasoningTimer 统计一次调用中模型思考（reasoning）的总时长。
// 思考时长只能通过 stream 回调精确测量；generate 模式无法获取，记为 0。
type reasoningTimer struct {
	mu     sync.Mutex           // 回调可能在工具执行 goroutine 中触发，加锁保证并发安全
	active map[string]time.Time // id -> 思考开始时间
	total  time.Duration        // 累计思考时长
}

func (t *reasoningTimer) start(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active[id] = time.Now()
}

func (t *reasoningTimer) end(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if started, ok := t.active[id]; ok {
		t.total += time.Since(started)
		delete(t.active, id)
	}
}

func (t *reasoningTimer) duration() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.total
}

// wrapReasoningTimer 包装 OnReasoningStart/OnReasoningEnd 回调统计思考时长。
func (a *Agent) wrapReasoningTimer(call *fantasy.AgentStreamCall) *reasoningTimer {
	timer := &reasoningTimer{active: make(map[string]time.Time)}

	onStart := call.OnReasoningStart
	call.OnReasoningStart = func(id string, reasoning fantasy.ReasoningContent) error {
		timer.start(id)
		if onStart != nil {
			return onStart(id, reasoning)
		}
		return nil
	}

	onEnd := call.OnReasoningEnd
	call.OnReasoningEnd = func(id string, reasoning fantasy.ReasoningContent) error {
		timer.end(id)
		if onEnd != nil {
			return onEnd(id, reasoning)
		}
		return nil
	}

	return timer
}

// record 组装 TurnUsage 并写入记录器。
// 记录失败只记 warning 日志，不影响回答结果返回。
func (a *Agent) record(
	ctx context.Context,
	mode string,
	startedAt time.Time,
	timer *reasoningTimer,
	result *fantasy.AgentResult,
	err error,
) {
	if a.recorder == nil {
		return
	}

	finishedAt := time.Now()

	turn := token.TurnUsage{
		Provider:      a.provider,
		Model:         a.model,
		Mode:          mode,
		StartedAt:     startedAt,
		FinishedAt:    finishedAt,
		TotalDuration: finishedAt.Sub(startedAt),
	}

	if a.conversationIDFunc != nil {
		turn.ConversationID = a.conversationIDFunc(ctx)
	}
	if a.messageIDFunc != nil {
		turn.MessageID = a.messageIDFunc(ctx)
	}
	if timer != nil {
		turn.ReasoningDuration = timer.duration()
	}
	if err != nil {
		turn.Err = err.Error()
	}

	if result != nil {
		turn.FinishReason = string(result.Response.FinishReason)
		turn.Total = token.FromFantasyUsage(result.TotalUsage)

		// 逐 step 记录 token 用量与工具调用次数，并累加本轮总数
		if len(result.Steps) > 0 {
			turn.Steps = make([]token.StepUsage, 0, len(result.Steps))
			for i, step := range result.Steps {
				toolCalls := len(step.Response.Content.ToolCalls())
				turn.ToolCalls += toolCalls
				turn.Steps = append(turn.Steps, token.StepUsage{
					StepIndex: i + 1,
					Usage:     token.FromFantasyUsage(step.Response.Usage),
					ToolCalls: toolCalls,
				})
			}
		}
	}

	if err := a.recorder.RecordUsage(ctx, turn); err != nil {
		a.logger.Warn("record agent usage failed", "error", err)
	}
}
