package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"charm.land/fantasy"
)

func TestFromFantasyUsage(t *testing.T) {
	tests := []struct {
		name  string
		input fantasy.Usage
		want  NormalizedUsage
	}{
		{"missing", fantasy.Usage{}, NormalizedUsage{}},
		{"responses cache and reasoning", fantasy.Usage{InputTokens: 0, OutputTokens: 340, TotalTokens: 457, CacheReadTokens: 117, ReasoningTokens: 284}, NormalizedUsage{OutputTokens: 340, TotalTokens: 457, CacheHitTokens: 117, ReasoningTokens: 284}},
		{"chat reasoning", fantasy.Usage{InputTokens: 91, OutputTokens: 32, TotalTokens: 123, ReasoningTokens: 22}, NormalizedUsage{InputTokens: 91, OutputTokens: 32, TotalTokens: 123, ReasoningTokens: 22}},
		{"usage reasoning", fantasy.Usage{InputTokens: 17, OutputTokens: 279, TotalTokens: 296, ReasoningTokens: 210}, NormalizedUsage{InputTokens: 17, OutputTokens: 279, TotalTokens: 296, ReasoningTokens: 210}},
		{"anthropic", fantasy.Usage{InputTokens: 24, OutputTokens: 146}, NormalizedUsage{InputTokens: 24, OutputTokens: 146, TotalTokens: 170}},
		{"fallback includes cache but not reasoning", fantasy.Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheCreationTokens: 40, ReasoningTokens: 5}, NormalizedUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 100, CacheHitTokens: 30, ReasoningTokens: 5}},
		{"clamp before fallback", fantasy.Usage{InputTokens: -10, OutputTokens: 20, TotalTokens: -1, CacheReadTokens: -2, CacheCreationTokens: -3, ReasoningTokens: -4}, NormalizedUsage{OutputTokens: 20, TotalTokens: 20}},
		{"preserve reported total", fantasy.Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 99}, NormalizedUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 99}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FromFantasyUsage(tt.input); got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestNormalizedUsageJSONIncludesZeroDetails(t *testing.T) {
	data, err := json.Marshal(FromFantasyUsage(fantasy.Usage{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"reasoning_tokens":0`, `"cache_hit_tokens":0`} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("missing %s in %s", field, data)
		}
	}
}
