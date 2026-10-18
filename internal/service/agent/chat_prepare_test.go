package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openai"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
)

func TestFirstStepCompactsBeforeReferenceResolution(t *testing.T) {
	for _, window := range []int{10000, 0} {
		t.Run(fmt.Sprint(window), func(t *testing.T) {
			ctx, projectID, dir := seedReferenceProject(t)
			file := filepath.Join(dir, "note.txt")
			const after = "reference written after compaction"
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["tools"] != nil || strings.Contains(fmt.Sprint(body["messages"]), after) {
					t.Error("summarizer received tools or expanded references")
				}
				if err := os.WriteFile(file, []byte(after), 0600); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"summary","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"Previous task retained; inspect note."},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":10,"total_tokens":60}}`)
			}))
			defer server.Close()
			provider, err := openai.New(openai.WithBaseURL(server.URL), openai.WithAPIKey("fake"))
			if err != nil {
				t.Fatal(err)
			}
			model, err := provider.LanguageModel(ctx, "test")
			if err != nil {
				t.Fatal(err)
			}
			messages := []fantasy.Message{fantasy.NewSystemMessage("system"), fantasy.NewUserMessage(strings.Repeat("old ", 9000)), testAssistantMessage("old answer"), fantasy.NewUserMessage("inspect note")}
			before, err := json.Marshal(messages)
			if err != nil {
				t.Fatal(err)
			}
			c := &contextCompactor{window: window, percent: 90, outputReserve: 1000}
			prompt := &chatPrompt{requestPrompt: "inspect note", projectID: projectID, references: []dtochat.Reference{{Type: "file", Path: "note.txt"}}}
			state := &chatPreparation{requestPrompt: prompt.requestPrompt}
			prepare := prepareChatStep(c, prompt, state)
			if window > 0 && referenceByteBudget(c, messages) != 0 {
				t.Fatal("fixture should require compaction before references fit")
			}
			_, first, err := prepare(ctx, fantasy.PrepareStepFunctionOptions{Messages: messages, Model: model})
			if err != nil {
				t.Fatal(err)
			}
			wanted := after
			if window == 0 {
				wanted = "reference evidence"
				if calls.Load() != 0 {
					t.Fatal("unknown window started compaction")
				}
			} else if calls.Load() == 0 || c.count != 1 {
				t.Fatal("history was not compacted first")
			}
			if !state.initialized || state.referenceSummary == nil || !strings.Contains(fmt.Sprint(first.Messages), wanted) || prompt.requestPrompt != "inspect note" {
				t.Fatal("first-step reference state incorrect")
			}
			unchanged, _ := json.Marshal(messages)
			if !reflect.DeepEqual(before, unchanged) {
				t.Fatal("original history mutated")
			}
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			count := calls.Load()
			next := append(append([]fantasy.Message(nil), messages...), testAssistantMessage("next work"))
			_, second, err := prepare(ctx, fantasy.PrepareStepFunctionOptions{Messages: next, Model: model, Steps: []fantasy.StepResult{{Response: fantasy.Response{Usage: fantasy.Usage{InputTokens: 100, OutputTokens: 10}}}}})
			if err != nil {
				t.Fatal(err)
			}
			printed := fmt.Sprint(second.Messages)
			if !strings.Contains(printed, wanted) || strings.Count(printed, wanted) != 1 || strings.Count(printed, "next work") != 1 || c.seen != len(next) || calls.Load() != count {
				t.Fatal("references reread or Fantasy sequence duplicated")
			}
		})
	}
}

func TestFirstStepCancellationDoesNotResolveReference(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	state := &chatPreparation{}
	_, _, err := prepareChatStep(&contextCompactor{}, &chatPrompt{projectID: "missing", references: []dtochat.Reference{{Type: "file", Path: "missing"}}}, state)(ctx, fantasy.PrepareStepFunctionOptions{})
	if !errors.Is(err, context.Canceled) || state.initialized {
		t.Fatal("cancellation became a successful preparation")
	}
}
