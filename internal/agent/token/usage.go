// Package agent 提供 Agent 运行信息记录模型：token 用量、耗时、思考时长、工具调用次数等。
package agent

import (
	"context"
	"time"

	"charm.land/fantasy"
)

// UsageRecorder 接收每次回答的运行信息记录。
type UsageRecorder interface {
	RecordUsage(ctx context.Context, usage TurnUsage) error
}

// NormalizedUsage 一次回答（或单个 step）的 token 用量。
// 字段经过 FromFantasyUsage 归一化：非负且 TotalTokens 始终有效。
type NormalizedUsage struct {
	InputTokens    int64 `json:"input_tokens"`     // 输入 token 数量
	OutputTokens   int64 `json:"output_tokens"`    // 输出 token 数量
	TotalTokens    int64 `json:"total_tokens"`     // 总 token 数量
	CacheHitTokens int64 `json:"cache_hit_tokens"` // 缓存命中的 token 数量（来自 CacheRead）
}

// FromFantasyUsage 将 fantasy.Usage 归一化为 NormalizedUsage。
// TotalTokens 缺失时用 input+output 兜底；所有字段做非负钳制。
func FromFantasyUsage(u fantasy.Usage) NormalizedUsage {
	if u.TotalTokens <= 0 {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
	if u.InputTokens < 0 {
		u.InputTokens = 0
	}
	if u.OutputTokens < 0 {
		u.OutputTokens = 0
	}
	if u.TotalTokens < 0 {
		u.TotalTokens = 0
	}
	if u.CacheReadTokens < 0 {
		u.CacheReadTokens = 0
	}

	return NormalizedUsage{
		InputTokens:    u.InputTokens,
		OutputTokens:   u.OutputTokens,
		TotalTokens:    u.TotalTokens,
		CacheHitTokens: u.CacheReadTokens,
	}
}

// StepUsage 单个 step（一次模型调用）的运行信息。
type StepUsage struct {
	StepIndex int             `json:"step_index"` // step 序号，从 1 开始
	Usage     NormalizedUsage `json:"usage"`      // 该 step 的 token 用量
	ToolCalls int             `json:"tool_calls"` // 该 step 发出的工具调用次数
}

// TurnUsage 一次回答（一次 Generate/Stream 调用）的运行信息。
type TurnUsage struct {
	ConversationID string `json:"conversation_id,omitempty"` // 会话 ID
	MessageID      string `json:"message_id,omitempty"`      // 消息 ID

	Provider string `json:"provider,omitempty"` // 提供商名称
	Model    string `json:"model,omitempty"`    // 模型名称

	Mode string `json:"mode"` // 调用模式：generate / stream

	StartedAt  time.Time `json:"started_at"`  // 开始时间
	FinishedAt time.Time `json:"finished_at"` // 结束时间

	TotalDuration     time.Duration `json:"total_duration"`               // 单次回答总时长
	ReasoningDuration time.Duration `json:"reasoning_duration,omitempty"` // 思考时长（仅 stream 模式可测，generate 为 0）

	ToolCalls    int             `json:"tool_calls"`              // 本轮工具调用总次数
	FinishReason string          `json:"finish_reason,omitempty"` // 结束原因：stop / tool-calls / length / error 等
	Total        NormalizedUsage `json:"total"`                   // 本轮合计 token 用量
	Steps        []StepUsage     `json:"steps,omitempty"`         // 各 step 明细

	Err string `json:"err,omitempty"` // 错误信息（失败时）
}

type contextKey string

const (
	conversationIDKey contextKey = "conversation_id"
	messageIDKey      contextKey = "message_id"
)

// WithConversationID 将会话 ID 注入 context，供运行时记录时读取。
func WithConversationID(ctx context.Context, conversationID string) context.Context {
	return context.WithValue(ctx, conversationIDKey, conversationID)
}

// ConversationIDFromContext 从 context 读取会话 ID，未设置时返回空串。
func ConversationIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(conversationIDKey).(string)
	return v
}

// WithMessageID 将消息 ID 注入 context，供运行时记录时读取。
func WithMessageID(ctx context.Context, messageID string) context.Context {
	return context.WithValue(ctx, messageIDKey, messageID)
}

// MessageIDFromContext 从 context 读取消息 ID，未设置时返回空串。
func MessageIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(messageIDKey).(string)
	return v
}
