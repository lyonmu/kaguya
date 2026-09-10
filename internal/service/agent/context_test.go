package agent

import (
	"errors"
	"testing"

	"charm.land/fantasy"
)

func TestCompletedContextTokens(t *testing.T) {
	if completedContextTokens(fantasy.Usage{}) != nil {
		t.Fatal("missing usage must stay unknown")
	}
	got := completedContextTokens(fantasy.Usage{InputTokens: 100, OutputTokens: 30, CacheReadTokens: 50, CacheCreationTokens: 20, ReasoningTokens: 10, TotalTokens: 9999})
	if got == nil || *got != 200 {
		t.Fatalf("context double counted reasoning or used cumulative total: %v", got)
	}
}

func TestConversationContextUsesLatestCompletedModel(t *testing.T) {
	ctx, _ := setupChatTest(t)
	svc := &AgentSvc{}
	first := testCompletedTurn("context", 0)
	first.ModelID, first.ContextWindow = "large", 1000
	tokens := int64(450)
	first.ContextTokens = &tokens
	first.Usage.TotalTokens = 450
	if err := saveCompletedTurn(ctx, first); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ConversationContext(ctx, "context")
	if err != nil || got.Percent == nil || *got.Percent != 50 || *got.MaxWindowPercent != 45 {
		t.Fatalf("context=%+v err=%v", got, err)
	}
	second := testCompletedTurn("context", 1)
	second.ModelID, second.ContextWindow, second.ContextTokens = "small", 500, &tokens
	second.Usage.TotalTokens = 450
	if err := saveCompletedTurn(ctx, second); err != nil {
		t.Fatal(err)
	}
	got, err = svc.ConversationContext(ctx, "context")
	if err != nil || got.ModelID != "small" || got.TurnIndex != 2 || got.EffectiveWindow != 450 || *got.Percent != 100 || *got.ContextTokens != 450 {
		t.Fatalf("latest context=%+v err=%v", got, err)
	}
	second.Version, second.ContextWindow = 2, 100
	if err := saveCompletedTurn(ctx, second); err != nil {
		t.Fatal(err)
	}
	got, err = svc.ConversationContext(ctx, "context")
	if err != nil || *got.Percent != 500 {
		t.Fatalf("overflow percentage should not be clamped: %+v %v", got, err)
	}
	legacy := testCompletedTurn("legacy", 0)
	legacy.Usage.TotalTokens = 0
	if err := saveCompletedTurn(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	got, err = svc.ConversationContext(ctx, "legacy")
	if err != nil || got.ContextTokens != nil || got.Percent != nil {
		t.Fatalf("legacy=%+v %v", got, err)
	}
	if err := svc.ConversationDelete(ctx, "context"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"context", "missing"} {
		if _, err := svc.ConversationContext(ctx, id); !errors.Is(err, ErrConversationNotFound) {
			t.Fatalf("%s: %v", id, err)
		}
	}
}

func TestContextOutputLimit(t *testing.T) {
	for _, tt := range []struct {
		window, configured int
		want               int64
	}{{1000, 500, 100}, {1000, 50, 50}, {0, 50, 50}, {0, 0, 0}} {
		got := contextOutputLimit(tt.window, tt.configured)
		if tt.want == 0 {
			if got != nil {
				t.Fatal("unknown limit must be omitted")
			}
			continue
		}
		if got == nil || *got != tt.want {
			t.Fatalf("limit=%v want=%d", got, tt.want)
		}
	}
}

func TestPausedContextIncludesOnlyUncountedToolResults(t *testing.T) {
	tool := fantasy.Message{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.ToolResultPart{ToolCallID: "read", Output: fantasy.ToolResultOutputContentText{Text: "reference content"}}}}
	result := &fantasy.AgentResult{
		Response: fantasy.Response{Usage: fantasy.Usage{InputTokens: 999}},
		Steps:    []fantasy.StepResult{{Response: fantasy.Response{Usage: fantasy.Usage{InputTokens: 100, OutputTokens: 20}}, Messages: []fantasy.Message{fantasy.NewUserMessage("already counted"), tool}}},
	}
	if got := completedResultContextTokens(result, false); got == nil || *got != 120 {
		t.Fatalf("must use latest step usage: %v", got)
	}
	if got := completedResultContextTokens(result, true); got == nil || *got != 120+estimateMessages([]fantasy.Message{tool}) {
		t.Fatalf("must add only unread tool output estimate: %v", got)
	}
	result.Steps[0].Usage = fantasy.Usage{}
	if completedResultContextTokens(result, true) != nil {
		t.Fatal("unreported usage must remain unknown")
	}
}
