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

func BenchmarkCompactionPrepareWithActualUsage(b *testing.B) {
	messages := make([]fantasy.Message, 10000)
	for i := range messages {
		messages[i] = fantasy.NewUserMessage("history that should not be serialized when provider usage is available")
	}
	opts := fantasy.PrepareStepFunctionOptions{
		Messages: messages,
		Steps:    []fantasy.StepResult{{Response: fantasy.Response{Usage: fantasy.Usage{InputTokens: 100, OutputTokens: 10}}}},
	}
	for _, baseline := range []bool{true, false} {
		name := "lazy"
		if baseline {
			name = "baseline"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				compactor := &contextCompactor{window: 1000000}
				if baseline {
					compactor.messages = append(compactor.messages, opts.Messages...)
					if referenceContextTokens(compactor, opts, opts.Messages) >= 900000 {
						b.Fatal("unexpected compaction")
					}
				} else if _, _, err := compactor.prepare(context.Background(), opts); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// The pre-optimization formula intentionally estimates the complete history first.
func referenceContextTokens(c *contextCompactor, opts fantasy.PrepareStepFunctionOptions, trailing []fantasy.Message) int64 {
	tokens := estimateMessages(c.messages) + c.toolTokens
	if len(opts.Steps) > 0 {
		if actual := completedContextTokens(opts.Steps[len(opts.Steps)-1].Usage); actual != nil {
			var results []fantasy.Message
			for _, message := range trailing {
				if message.Role == fantasy.MessageRoleTool {
					results = append(results, message)
				}
			}
			tokens = *actual + estimateMessages(results)
		}
	} else if c.lastTokens != nil {
		tokens = max(tokens, *c.lastTokens)
	}
	return tokens
}

func TestCompactionLazyFormulaBoundaries(t *testing.T) {
	actual := fantasy.StepResult{Response: fantasy.Response{Usage: fantasy.Usage{InputTokens: 100, CacheReadTokens: 20, OutputTokens: 5}}}
	for _, steps := range [][]fantasy.StepResult{nil, {actual}, {actual, {}}, {{Response: fantasy.Response{Usage: fantasy.Usage{TotalTokens: 1000}}}}} {
		for _, previous := range []int64{-1, 0, 30, 900} {
			for _, withTool := range []bool{false, true} {
				messages := []fantasy.Message{fantasy.NewUserMessage(strings.Repeat("x", 600))}
				if withTool {
					messages = append(messages, fantasy.Message{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.ToolResultPart{ToolCallID: "t", Output: fantasy.ToolResultOutputContentText{Text: "tool output"}}}})
				}
				for _, seen := range []int{0, 1} {
					c := &contextCompactor{messages: messages, toolTokens: 7}
					if previous >= 0 {
						c.lastTokens = &previous
					}
					opts := fantasy.PrepareStepFunctionOptions{Messages: messages, Steps: steps}
					tokens := referenceContextTokens(c, opts, messages[seen:])
					for _, delta := range []int64{-1, 0, 1} {
						copy := *c
						copy.window, copy.percent, copy.seen = int(2*(tokens+delta)), 50, seen
						copy.messages = append([]fantasy.Message{}, messages[:seen]...)
						_, _, err := copy.prepare(context.Background(), opts)
						if (err != nil) != (delta <= 0) {
							t.Fatalf("steps=%+v previous=%d tool=%v seen=%d tokens=%d delta=%d err=%v", steps, previous, withTool, seen, tokens, delta, err)
						}
					}
				}
			}
		}
	}
}

type serializationProbe struct{ calls int }

func (*serializationProbe) Options()                       {}
func (p *serializationProbe) MarshalJSON() ([]byte, error) { p.calls++; return []byte(`{}`), nil }
func (*serializationProbe) UnmarshalJSON([]byte) error     { return nil }

func TestCompactionSkipsHistorySerializationOnlyWithActualStepUsage(t *testing.T) {
	for _, actual := range []bool{false, true} {
		probe := &serializationProbe{}
		message := fantasy.NewUserMessage("history")
		message.ProviderOptions = fantasy.ProviderOptions{"probe": probe}
		step := fantasy.StepResult{}
		if actual {
			step.Usage.InputTokens = 1
		}
		c := &contextCompactor{window: 100000}
		_, _, err := c.prepare(context.Background(), fantasy.PrepareStepFunctionOptions{Messages: []fantasy.Message{message}, Steps: []fantasy.StepResult{step}})
		if err != nil || (probe.calls == 0) != actual {
			t.Fatalf("actual=%v serializations=%d err=%v", actual, probe.calls, err)
		}
	}
	c := &contextCompactor{seen: 99}
	if _, _, err := c.prepare(context.Background(), fantasy.PrepareStepFunctionOptions{}); err != nil || c.seen != 99 {
		t.Fatal("unknown window no longer skips preparation")
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
