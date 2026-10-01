package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
)

func TestComputeContextBudget(t *testing.T) {
	unknown := computeContextBudget(contextBudgetInput{systemTokens: 10})
	if unknown.known || unknown.threshold != 0 || unknown.referenceBudget != 0 {
		t.Fatalf("unknown window must not fabricate capacity: %+v", unknown)
	}

	// 大 system/历史把剩余预算压到 0：引用预算也必须为 0，而不是负数。
	exhausted := computeContextBudget(contextBudgetInput{
		window: 1000, compactionPercent: 90, systemTokens: 700, toolSchemaTokens: 100,
		historyTokens: 100, userTokens: 50, memoryTokens: 50, outputReserve: 100,
	})
	if !exhausted.known || exhausted.threshold != 900 || exhausted.remaining != 0 || exhausted.referenceBudget != 0 {
		t.Fatalf("exhausted budget=%+v", exhausted)
	}

	// 引用最多使用剩余预算的 25%。
	budget := computeContextBudget(contextBudgetInput{
		window: 10000, compactionPercent: 80, systemTokens: 1000, toolSchemaTokens: 1000,
		historyTokens: 2000, userTokens: 500, memoryTokens: 500, outputReserve: 1000,
	})
	if budget.threshold != 8000 || budget.remaining != 2000 || budget.referenceBudget != 500 {
		t.Fatalf("budget=%+v", budget)
	}
}

func TestContextFingerprintEquality(t *testing.T) {
	base := contextFingerprint{modelKey: "m", instructionHash: "i", toolSchemaHash: "t", memorySelectionKey: "s"}
	if !base.equal(base) {
		t.Fatal("identical fingerprints differ")
	}
	for name, changed := range map[string]contextFingerprint{
		"model":       {modelKey: "other", instructionHash: "i", toolSchemaHash: "t", memorySelectionKey: "s"},
		"instruction": {modelKey: "m", instructionHash: "other", toolSchemaHash: "t", memorySelectionKey: "s"},
		"schema":      {modelKey: "m", instructionHash: "i", toolSchemaHash: "other", memorySelectionKey: "s"},
		"memory":      {modelKey: "m", instructionHash: "i", toolSchemaHash: "t", memorySelectionKey: "other"},
	} {
		if base.equal(changed) {
			t.Errorf("%s change did not invalidate the fingerprint", name)
		}
	}
}

type fingerprintTool struct {
	info fantasy.ToolInfo
}

func (f fingerprintTool) Info() fantasy.ToolInfo { return f.info }
func (f fingerprintTool) Run(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.ToolResponse{}, nil
}
func (f fingerprintTool) ProviderOptions() fantasy.ProviderOptions   { return nil }
func (f fingerprintTool) SetProviderOptions(fantasy.ProviderOptions) {}

func TestToolSchemaFingerprintIsStableAndSensitive(t *testing.T) {
	first := fingerprintTool{info: fantasy.ToolInfo{Name: "read", Parameters: map[string]any{"type": "object"}}}
	second := fingerprintTool{info: fantasy.ToolInfo{Name: "edit", Parameters: map[string]any{"type": "object"}}}
	if toolSchemaFingerprint([]fantasy.AgentTool{first, second}) != toolSchemaFingerprint([]fantasy.AgentTool{second, first}) {
		t.Fatal("fingerprint depends on registration order")
	}
	changedSchema := fingerprintTool{info: fantasy.ToolInfo{Name: "read", Parameters: map[string]any{"type": "array"}}}
	if toolSchemaFingerprint([]fantasy.AgentTool{first, second}) == toolSchemaFingerprint([]fantasy.AgentTool{changedSchema, second}) {
		t.Fatal("schema change did not change the fingerprint")
	}
	if toolSchemaFingerprint(nil) == "" {
		t.Fatal("empty tool set must still have a stable fingerprint")
	}
}

func TestCompactorPrefersBudgetThreshold(t *testing.T) {
	compactor := &contextCompactor{window: 1000, percent: 90, budget: computeContextBudget(contextBudgetInput{window: 1000, compactionPercent: 50})}
	if got := compactor.thresholdTokens(); got != 500 {
		t.Fatalf("threshold=%d", got)
	}
	fallback := &contextCompactor{window: 1000, percent: 90}
	if got := fallback.thresholdTokens(); got != 900 {
		t.Fatalf("fallback threshold=%d", got)
	}
	if got := (&contextCompactor{}).thresholdTokens(); got != 0 {
		t.Fatalf("unknown window threshold=%d", got)
	}
}
