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
	if err := saveCompletedTurn(ctx, first); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ConversationContext(ctx, "context")
	if err != nil || got.Percent == nil || *got.Percent != 50 || *got.MaxWindowPercent != 45 {
		t.Fatalf("context=%+v err=%v", got, err)
	}
	second := testCompletedTurn("context", 1)
	second.ModelID, second.ContextWindow, second.ContextTokens = "small", 500, &tokens
	if err := saveCompletedTurn(ctx, second); err != nil {
		t.Fatal(err)
	}
	got, err = svc.ConversationContext(ctx, "context")
	if err != nil || got.ModelID != "small" || got.TurnIndex != 2 || got.EffectiveWindow != 450 || *got.Percent != 100 {
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
	if err := saveCompletedTurn(ctx, testCompletedTurn("legacy", 0)); err != nil {
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
