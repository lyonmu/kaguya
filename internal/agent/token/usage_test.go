package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"charm.land/fantasy"
)

// TestFromFantasyUsage 覆盖 TotalTokens 兜底、负数钳制与 CacheHit 映射。
func TestFromFantasyUsage(t *testing.T) {
	t.Run("total falls back to input+output", func(t *testing.T) {
		got := FromFantasyUsage(fantasy.Usage{InputTokens: 10, OutputTokens: 20})
		if got.TotalTokens != 30 {
			t.Fatalf("TotalTokens = %d, want 30", got.TotalTokens)
		}
		if got.InputTokens != 10 || got.OutputTokens != 20 {
			t.Fatalf("Input/Output = %d/%d, want 10/20", got.InputTokens, got.OutputTokens)
		}
		if got.CacheHitTokens != 0 {
			t.Fatalf("CacheHitTokens = %d, want 0", got.CacheHitTokens)
		}
	})

	t.Run("keeps explicit total", func(t *testing.T) {
		got := FromFantasyUsage(fantasy.Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 50})
		if got.TotalTokens != 50 {
			t.Fatalf("TotalTokens = %d, want 50", got.TotalTokens)
		}
	})

	t.Run("clamps negative values to zero", func(t *testing.T) {
		got := FromFantasyUsage(fantasy.Usage{
			InputTokens: -1, OutputTokens: -2, TotalTokens: -3, CacheReadTokens: -4,
		})
		if got.InputTokens != 0 || got.OutputTokens != 0 || got.TotalTokens != 0 || got.CacheHitTokens != 0 {
			t.Fatalf("expected all zeros, got %+v", got)
		}
	})

	t.Run("cache hit maps from cache read", func(t *testing.T) {
		got := FromFantasyUsage(fantasy.Usage{InputTokens: 1, OutputTokens: 1, CacheReadTokens: 42})
		if got.CacheHitTokens != 42 {
			t.Fatalf("CacheHitTokens = %d, want 42", got.CacheHitTokens)
		}
	})
}

// TestContextHelpers 覆盖会话/消息 ID 的注入与读取。
func TestContextHelpers(t *testing.T) {
	ctx := WithConversationID(context.Background(), "conv-1")
	ctx = WithMessageID(ctx, "msg-1")

	if got := ConversationIDFromContext(ctx); got != "conv-1" {
		t.Fatalf("ConversationIDFromContext = %q, want %q", got, "conv-1")
	}
	if got := MessageIDFromContext(ctx); got != "msg-1" {
		t.Fatalf("MessageIDFromContext = %q, want %q", got, "msg-1")
	}
	if got := ConversationIDFromContext(context.Background()); got != "" {
		t.Fatalf("empty ctx ConversationID = %q, want empty", got)
	}
}

// TestTurnUsageJSON 校验关键 JSON 字段名，防止序列化歧义。
func TestTurnUsageJSON(t *testing.T) {
	u := TurnUsage{
		Provider:          "openai",
		Model:             "gpt-4o",
		Mode:              "stream",
		StartedAt:         time.Unix(0, 1),
		FinishedAt:        time.Unix(0, 2),
		TotalDuration:     time.Second,
		ReasoningDuration: 500 * time.Millisecond,
		ToolCalls:         3,
		FinishReason:      "stop",
		Total:             NormalizedUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30, CacheHitTokens: 5},
		Steps:             []StepUsage{{StepIndex: 1, Usage: NormalizedUsage{InputTokens: 10}, ToolCalls: 3}},
	}

	b, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{
		"total_duration", "reasoning_duration", "tool_calls", "finish_reason",
		"steps", "mode", "started_at", "finished_at",
	} {
		if _, ok := m[k]; !ok {
			t.Errorf("JSON 缺少字段 %q", k)
		}
	}
	total, ok := m["total"].(map[string]any)
	if !ok {
		t.Fatalf("JSON 缺少字段 %q", "total")
	}
	for _, k := range []string{
		"input_tokens", "output_tokens", "total_tokens", "cache_hit_tokens",
	} {
		if _, ok := total[k]; !ok {
			t.Errorf("JSON 缺少字段 %q", k)
		}
	}

	// 反序列化到类型化结构，校验字段值正确性（往返一致），而非仅存在性
	var got TurnUsage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal typed: %v", err)
	}
	if !got.StartedAt.Equal(u.StartedAt) || !got.FinishedAt.Equal(u.FinishedAt) {
		t.Fatalf("started/finished_at = %v/%v, want %v/%v", got.StartedAt, got.FinishedAt, u.StartedAt, u.FinishedAt)
	}
	if got.TotalDuration != u.TotalDuration || got.ReasoningDuration != u.ReasoningDuration {
		t.Fatalf("total/reasoning_duration = %v/%v, want %v/%v", got.TotalDuration, got.ReasoningDuration, u.TotalDuration, u.ReasoningDuration)
	}
	if got.ToolCalls != u.ToolCalls || got.FinishReason != u.FinishReason {
		t.Fatalf("tool_calls/finish_reason = %d/%q, want %d/%q", got.ToolCalls, got.FinishReason, u.ToolCalls, u.FinishReason)
	}
	if got.Total != u.Total {
		t.Fatalf("total = %+v, want %+v", got.Total, u.Total)
	}
	if len(got.Steps) != 1 || got.Steps[0].StepIndex != 1 || got.Steps[0].ToolCalls != 3 {
		t.Fatalf("steps = %+v, want 1 step with index=1 tool_calls=3", got.Steps)
	}
}
