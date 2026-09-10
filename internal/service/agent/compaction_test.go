package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openai"
)

func TestCompactionSnapshotAndContinuation(t *testing.T) {
	ctx, client := setupChatTest(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["tools"] != nil {
			t.Error("summarizer received tools")
		}
		var bytes int
		for _, msg := range body["messages"].([]any) {
			bytes += len(msg.(map[string]any)["content"].(string))
		}
		if bytes+int(body["max_tokens"].(float64))+128 > 900 {
			t.Error("summarizer request exceeded bounded context")
		}
		fmt.Fprint(w, `{"id":"s","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"Goal: fix main.go; tests pending."},"finish_reason":"stop"}],"usage":{"prompt_tokens":900,"completion_tokens":20,"total_tokens":920}}`)
	}))
	defer server.Close()
	provider, err := openai.New(openai.WithBaseURL(server.URL), openai.WithAPIKey("test"))
	if err != nil {
		t.Fatal(err)
	}
	model, err := provider.LanguageModel(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	messages := []fantasy.Message{fantasy.NewSystemMessage("system"), fantasy.NewUserMessage(strings.Repeat("old ", 1000)), testAssistantMessage("old answer"), fantasy.NewUserMessage("continue")}
	compactor := &contextCompactor{window: 1000}
	_, prepared, err := compactor.prepare(ctx, fantasy.PrepareStepFunctionOptions{Messages: messages, Model: model})
	if err != nil || calls < 2 || compactor.count != 1 {
		t.Fatalf("compact: calls=%d err=%v", calls, err)
	}
	firstCalls := calls
	if strings.Contains(fmt.Sprint(prepared.Messages), strings.Repeat("old ", 20)) {
		t.Fatal("old transcript retained")
	}
	next := append(append([]fantasy.Message{}, messages...), testAssistantMessage("new work"))
	_, prepared, err = compactor.prepare(ctx, fantasy.PrepareStepFunctionOptions{Messages: next, Model: model, Steps: []fantasy.StepResult{{Response: fantasy.Response{Usage: fantasy.Usage{InputTokens: 100, OutputTokens: 10}}}}})
	if err != nil || calls != firstCalls || !strings.Contains(fmt.Sprint(prepared.Messages), "new work") {
		t.Fatalf("continuation=%+v %v", prepared, err)
	}
	result := &fantasy.AgentResult{Steps: []fantasy.StepResult{{Messages: []fantasy.Message{testAssistantMessage("done")}}}}
	turn := testCompletedTurn("compact", 0)
	instructions := "persistent agent instructions"
	turn.AgentInstructions = &instructions
	turn.ContextMessages = compactor.snapshot(result)
	turn.CompactionCount = compactor.count
	if err := saveCompletedTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	loaded, version, err := loadConversation(ctx, "compact")
	if err != nil || version != 1 || len(loaded) != len(turn.ContextMessages) || loaded[0].Role == fantasy.MessageRoleSystem {
		t.Fatalf("loaded=%+v %v", loaded, err)
	}
	stored, err := client.KaguyaChatTurn.Query().Only(ctx)
	if err != nil || len(stored.Messages) != len(turn.Messages) || stored.CompactionCount != 1 {
		t.Fatalf("original history lost: %v", err)
	}
	// A cut inside a tool-result group must move back to its assistant call.
	paired := []fantasy.Message{
		fantasy.NewSystemMessage("system"), fantasy.NewUserMessage(strings.Repeat("old ", 1000)),
		{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.ToolCallPart{ToolCallID: "call-1", ToolName: "read", Input: `{"path":"main.go"}`}}},
		{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.ToolResultPart{ToolCallID: "call-1", Output: fantasy.ToolResultOutputContentText{Text: strings.Repeat("data ", 80)}}}},
	}
	pairCompactor := &contextCompactor{window: 1000}
	_, pairResult, pairErr := pairCompactor.prepare(ctx, fantasy.PrepareStepFunctionOptions{Messages: paired, Model: model})
	if pairErr != nil {
		t.Fatal(pairErr)
	}
	if len(pairResult.Messages) != 4 || pairResult.Messages[2].Role != fantasy.MessageRoleAssistant || pairResult.Messages[3].Role != fantasy.MessageRoleTool {
		t.Fatalf("tool pair split: %+v", pairResult.Messages)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	failed := &contextCompactor{window: 1000}
	if _, _, err := failed.prepare(cancelled, fantasy.PrepareStepFunctionOptions{Messages: messages, Model: model}); err == nil || failed.count != 0 {
		t.Fatalf("cancelled compaction committed: %v", err)
	}
	turn2 := testCompletedTurn("compact", 1)
	if err := saveCompletedTurn(ctx, turn2); err != nil {
		t.Fatal(err)
	}
	loaded, _, err = loadConversation(ctx, "compact")
	if err != nil || len(loaded) != len(turn.ContextMessages)+len(turn2.Messages) {
		t.Fatalf("snapshot append: %v", err)
	}
	if got, err := conversationInstructions(ctx, "compact", nil, "missing-project"); err != nil || got != instructions {
		t.Fatalf("compaction lost instruction snapshot: %q %v", got, err)
	}
}

func TestCompactionRejectsUncompactableInput(t *testing.T) {
	c := &contextCompactor{window: 100}
	_, _, err := c.prepare(context.Background(), fantasy.PrepareStepFunctionOptions{Messages: []fantasy.Message{fantasy.NewUserMessage(strings.Repeat("x", 1000))}})
	if err == nil {
		t.Fatal("oversized single input accepted")
	}
}

func TestConfiguredCompactionTriggersAtBoundary(t *testing.T) {
	for _, percent := range []int{50, 80, 95} {
		for _, offset := range []int64{-1, 0, 1} {
			tokens := int64(percent*10) + offset
			c := &contextCompactor{window: 1000, percent: percent, lastTokens: &tokens}
			_, _, err := c.prepare(context.Background(), fantasy.PrepareStepFunctionOptions{Messages: []fantasy.Message{fantasy.NewUserMessage("single input")}})
			if (err != nil) != (offset >= 0) {
				t.Fatalf("percent=%d tokens=%d error=%v", percent, tokens, err)
			}
			if offset >= 0 && !strings.Contains(err.Error(), fmt.Sprintf("%d%%", percent)) {
				t.Fatal("error reported wrong threshold")
			}
		}
	}
}

func testAssistantMessage(text string) fantasy.Message {
	return fantasy.Message{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.TextPart{Text: text}}}
}
