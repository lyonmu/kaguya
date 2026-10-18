package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/agent/permission"
	"github.com/lyonmu/kaguya/internal/consts"
	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
)

func fingerprintExecution() chatExecution {
	percent := 90
	return chatExecution{conversationID: "fingerprint", permissionMode: permission.ModeAutoApproval,
		target: &chatTarget{info: &dtosystem.SystemInfoResp{SystemInfoSaveReq: dtosystem.SystemInfoSaveReq{ContextCompactionPercent: &percent}},
			model:    &ent.KaguyaModelsInfo{ID: "local-model", ModelID: "upstream", APIProtocol: consts.ProviderProtocol("openai-chat"), RequestPath: "/v1/chat/completions", TokenContextWindow: 10000, TokenMaxOutputTokens: 1000},
			provider: &ent.KaguyaProviderInfo{ID: "provider", ProviderType: consts.ProviderType("openai"), BaseURL: "https://example.test/v1", APIKey: "fake-key"}},
		prompt: &chatPrompt{requestPrompt: "continue", system: "complete system", instructions: "project instructions", instructionsFormat: "split", globalVersion: 3,
			tools:  []fantasy.AgentTool{fingerprintTool{info: fantasy.ToolInfo{Name: "read", Description: "read text", Parameters: map[string]any{"path": map[string]any{"type": "string"}}}}},
			memory: memoryTurn{refs: []dtomemory.TurnMemoryRef{{PageID: "page", Version: 1}}, estimated: 10, retriever: 2}}}
}

func cloneFingerprintExecution(base chatExecution) chatExecution {
	next := base
	target, model, provider, info, prompt := *base.target, *base.target.model, *base.target.provider, *base.target.info, *base.prompt
	percent := *info.ContextCompactionPercent
	info.ContextCompactionPercent = &percent
	target.model, target.provider, target.info = &model, &provider, &info
	prompt.tools = append([]fantasy.AgentTool(nil), prompt.tools...)
	prompt.memory.refs = append([]dtomemory.TurnMemoryRef(nil), prompt.memory.refs...)
	next.target, next.prompt = &target, &prompt
	return next
}

func TestContextFingerprintPersistenceAndInvalidation(t *testing.T) {
	ctx, client := setupChatTest(t)
	svc := &AgentSvc{}
	base := fingerprintExecution()
	initial, err := svc.newContextCompactor(ctx, base, base.target.info)
	if err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{initial.fingerprint.configurationHash, initial.fingerprint.systemHash, initial.fingerprint.instructionHash, initial.fingerprint.toolSchemaHash, initial.fingerprint.memorySelectionKey, initial.fingerprint.digest()} {
		if len(hash) != 64 {
			t.Fatal("persistent fingerprint must use full SHA-256")
		}
	}
	saved := testCompletedTurn(base.conversationID, 0)
	tokens := int64(9876)
	saved.ContextTokens = &tokens
	saved.ContextFingerprintVersion = contextFingerprintVersion
	saved.ContextFingerprint = initial.fingerprint.digest()
	if err := saveCompletedTurn(ctx, saved); err != nil {
		t.Fatal(err)
	}
	base.version = 1
	check := func(t *testing.T, exec chatExecution, wantReuse bool) {
		t.Helper()
		c, err := svc.newContextCompactor(ctx, exec, exec.target.info)
		if err != nil {
			t.Fatal(err)
		}
		if (c.lastTokens != nil) != wantReuse {
			t.Fatalf("baseline reuse=%v want=%v", c.lastTokens != nil, wantReuse)
		}
		if wantReuse && *c.lastTokens != tokens+estimateMessages([]fantasy.Message{fantasy.NewUserMessage(exec.prompt.requestPrompt)}) {
			t.Fatal("wrong actual token baseline")
		}
	}
	check(t, base, true)
	changes := map[string]func(*chatExecution){
		"local model":     func(e *chatExecution) { e.target.model.ID = "another-local" },
		"provider":        func(e *chatExecution) { e.target.provider.ID = "another-provider" },
		"upstream":        func(e *chatExecution) { e.target.model.ModelID = "another-upstream" },
		"protocol":        func(e *chatExecution) { e.target.model.APIProtocol = "anthropic" },
		"provider type":   func(e *chatExecution) { e.target.provider.ProviderType = "anthropic" },
		"endpoint":        func(e *chatExecution) { e.target.provider.BaseURL = "https://another.test/v1" },
		"path":            func(e *chatExecution) { e.target.model.RequestPath = "/v2/responses" },
		"query":           func(e *chatExecution) { e.target.model.RequestPath = "/v1/chat/completions?credential=fake" },
		"reasoning":       func(e *chatExecution) { e.target.model.ReasoningEnabled = 1 },
		"effort":          func(e *chatExecution) { e.target.model.ReasoningEffort = "high" },
		"window":          func(e *chatExecution) { e.target.model.TokenContextWindow++ },
		"output":          func(e *chatExecution) { e.target.model.TokenMaxOutputTokens++ },
		"percent":         func(e *chatExecution) { *e.target.info.ContextCompactionPercent = 80 },
		"tool capability": func(e *chatExecution) { e.target.model.CapabilityToolUse = 1 },
		"vision":          func(e *chatExecution) { e.target.model.CapabilityVision = 1 },
		"structured":      func(e *chatExecution) { e.target.model.CapabilityStructuredOutput = 1 },
		"system":          func(e *chatExecution) { e.prompt.system += " updated" },
		"instructions":    func(e *chatExecution) { e.prompt.instructions += " updated" },
		"format":          func(e *chatExecution) { e.prompt.instructionsFormat = "legacy" },
		"schema": func(e *chatExecution) {
			e.prompt.tools = []fantasy.AgentTool{fingerprintTool{info: fantasy.ToolInfo{Name: "read", Parameters: map[string]any{"password": map[string]any{"type": "string"}}}}}
		},
		"description": func(e *chatExecution) {
			tool := e.prompt.tools[0].(fingerprintTool)
			tool.info.Description = "new contract"
			e.prompt.tools[0] = tool
		},
		"registration identity": func(e *chatExecution) {
			e.prompt.tools[0] = identityTool{AgentTool: e.prompt.tools[0], identity: "mcp:server:read:2:schema"}
		},
		"memory": func(e *chatExecution) { e.prompt.memory.refs[0].Version++ },
		"mode":   func(e *chatExecution) { e.permissionMode = permission.ModeFullApproval },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) { next := cloneFingerprintExecution(base); change(&next); check(t, next, false) })
	}
	next := cloneFingerprintExecution(base)
	next.prompt.globalVersion = 999
	next.target.provider.APIKey = "rotated-fake-key"
	check(t, next, true)
	row, err := client.KaguyaChatTurn.Query().Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{0, 1, 99} {
		if err := client.KaguyaChatTurn.UpdateOneID(row.ID).SetContextFingerprintVersion(version).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		check(t, base, false)
	}
	if err := client.KaguyaChatTurn.UpdateOneID(row.ID).SetContextFingerprintVersion(contextFingerprintVersion).ClearContextFingerprint().Exec(ctx); err != nil {
		t.Fatal(err)
	}
	check(t, base, false)
	if err := client.KaguyaChatTurn.UpdateOneID(row.ID).SetContextFingerprint("invalid").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	check(t, base, false)
	if err := client.KaguyaChatTurn.UpdateOneID(row.ID).SetContextFingerprint(initial.fingerprint.digest()).ClearContextTokens().Exec(ctx); err != nil {
		t.Fatal(err)
	}
	check(t, base, false)
	if err := client.KaguyaChatTurn.UpdateOneID(row.ID).SetContextTokens(tokens).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	running, err := beginTurn(ctx, turnStart{ConversationID: base.conversationID, UserContent: "failed question", StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := markTurnEnd(running.ID, kaguyachatturn.StatusFailed, time.Now()); err != nil {
		t.Fatal(err)
	}
	check(t, base, true)
	// 保存 running -> completed 的另一条事务路径；失败用户提问仍保留。
	running, err = beginTurn(ctx, turnStart{ConversationID: base.conversationID, UserContent: "next", StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	saved = testCompletedTurn(base.conversationID, 1)
	saved.TurnID = running.ID
	saved.ContextFingerprintVersion = contextFingerprintVersion
	saved.ContextFingerprint = initial.fingerprint.digest()
	saved.ContextTokens = &tokens
	if err := saveCompletedTurn(ctx, saved); err != nil {
		t.Fatal(err)
	}
	row, err = client.KaguyaChatTurn.Get(ctx, running.ID)
	if err != nil || row.ContextFingerprintVersion != 2 || row.ContextFingerprint == nil || *row.ContextFingerprint != initial.fingerprint.digest() {
		t.Fatalf("running fingerprint persistence failed: %v", err)
	}
}

type neverEncodeOptions struct{ encoded *bool }

func (neverEncodeOptions) Options() {}
func (v neverEncodeOptions) MarshalJSON() ([]byte, error) {
	*v.encoded = true
	return nil, context.Canceled
}
func (neverEncodeOptions) UnmarshalJSON([]byte) error { return nil }

type optionsFingerprintTool struct {
	fingerprintTool
	options fantasy.ProviderOptions
}

func (t optionsFingerprintTool) ProviderOptions() fantasy.ProviderOptions { return t.options }

func TestFingerprintUnknownOptionsNeverSerializeSecrets(t *testing.T) {
	ctx, _ := setupChatTest(t)
	base := fingerprintExecution()
	encoded := false
	base.prompt.tools = []fantasy.AgentTool{optionsFingerprintTool{fingerprintTool: base.prompt.tools[0].(fingerprintTool), options: fantasy.ProviderOptions{"unknown": neverEncodeOptions{&encoded}}}}
	c, err := (&AgentSvc{}).newContextCompactor(ctx, base, base.target.info)
	if err != nil || !c.fingerprint.baselineUnknown || encoded {
		t.Fatalf("unknown options encoded or baseline trusted: err=%v encoded=%v", err, encoded)
	}
	// 不可复用的摘要若已落库，下一轮恢复普通 options 也不能借用它的实际 token。
	saved := testCompletedTurn(base.conversationID, 0)
	tokens := int64(888)
	saved.ContextTokens = &tokens
	saved.ContextFingerprintVersion = contextFingerprintVersion
	saved.ContextFingerprint = c.fingerprint.digest()
	if err := saveCompletedTurn(ctx, saved); err != nil {
		t.Fatal(err)
	}
	base.version = 1
	base.prompt.tools = []fantasy.AgentTool{base.prompt.tools[0].(optionsFingerprintTool).fingerprintTool}
	knownCompactor, err := (&AgentSvc{}).newContextCompactor(ctx, base, base.target.info)
	if err != nil || knownCompactor.lastTokens != nil {
		t.Fatal("unknown prior options leaked into known baseline")
	}
	h, known, err := fingerprintURL("https://fake-user:fake-password@example.test/v1?credential=fake-secret")
	h2, _, err2 := fingerprintURL("https://example.test/v1")
	if err != nil || err2 != nil || known || h != h2 || strings.Contains(h, "fake") {
		t.Fatal("URL credentials entered fingerprint projection")
	}
	tools := []fantasy.AgentTool{base.prompt.tools[0], base.prompt.tools[0]}
	if _, _, err := effectiveToolsFingerprint(tools); err == nil {
		t.Fatal("duplicate tool names accepted")
	}
	bad := fingerprintTool{info: fantasy.ToolInfo{Name: "broken", Parameters: map[string]any{"invalid": make(chan int)}}}
	if _, _, err := effectiveToolsFingerprint([]fantasy.AgentTool{bad}); err == nil {
		t.Fatal("unencodable schema accepted")
	}
}
