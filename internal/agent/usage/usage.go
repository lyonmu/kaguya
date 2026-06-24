package usage

import (
	"context"
	"time"

	"charm.land/fantasy"
)

type NormalizedUsage struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`

	// 业务侧更好理解的别名。
	CacheHitTokens   int64 `json:"cache_hit_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

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
	if u.ReasoningTokens < 0 {
		u.ReasoningTokens = 0
	}
	if u.CacheCreationTokens < 0 {
		u.CacheCreationTokens = 0
	}
	if u.CacheReadTokens < 0 {
		u.CacheReadTokens = 0
	}

	return NormalizedUsage{
		InputTokens:         u.InputTokens,
		OutputTokens:        u.OutputTokens,
		TotalTokens:         u.TotalTokens,
		ReasoningTokens:     u.ReasoningTokens,
		CacheCreationTokens: u.CacheCreationTokens,
		CacheReadTokens:     u.CacheReadTokens,
		CacheHitTokens:      u.CacheReadTokens,
		CacheWriteTokens:    u.CacheCreationTokens,
	}
}

type StepUsage struct {
	StepIndex int             `json:"step_index"`
	Usage     NormalizedUsage `json:"usage"`
}

type TurnUsage struct {
	ConversationID string `json:"conversation_id,omitempty"`
	MessageID      string `json:"message_id,omitempty"`

	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	Mode string `json:"mode"` // generate / stream

	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	Latency    time.Duration `json:"latency"`

	Total NormalizedUsage `json:"total"`
	Steps []StepUsage     `json:"steps,omitempty"`

	Err string `json:"err,omitempty"`
}

type contextKey string

const (
	conversationIDKey contextKey = "conversation_id"
	messageIDKey      contextKey = "message_id"
)

func WithConversationID(ctx context.Context, conversationID string) context.Context {
	return context.WithValue(ctx, conversationIDKey, conversationID)
}

func ConversationIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(conversationIDKey).(string)
	return v
}

func WithMessageID(ctx context.Context, messageID string) context.Context {
	return context.WithValue(ctx, messageIDKey, messageID)
}

func MessageIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(messageIDKey).(string)
	return v
}
