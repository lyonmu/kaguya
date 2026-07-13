package agent_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"charm.land/fantasy"

	"github.com/lyonmu/kaguya/internal/agent"
	"github.com/lyonmu/kaguya/internal/agent/conversation"
	"github.com/lyonmu/kaguya/internal/agent/runtime"
	"github.com/lyonmu/kaguya/internal/agent/tools/files"
	"github.com/lyonmu/kaguya/internal/agent/usage"
)

// readmeFakeModel simulates a two-step tool-call flow:
// 1st Generate: asserts call.Prompt contains the user question, returns a
// read_file tool call for README.md with FinishReasonToolCalls.
// 2nd Generate: asserts call.Prompt contains a tool message whose
// ToolResultPart text includes "kaguya", then returns a text response.
type readmeFakeModel struct {
	t          *testing.T
	userPrompt string

	mu    sync.Mutex
	calls int
}

func (m *readmeFakeModel) Generate(_ context.Context, call fantasy.Call) (*fantasy.Response, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.mu.Unlock()

	switch n {
	case 1:
		if !promptContainsUserText(call.Prompt, m.userPrompt) {
			m.t.Errorf("first Generate: expected user message with %q in call.Prompt", m.userPrompt)
		}
		return &fantasy.Response{
			Content: fantasy.ResponseContent{
				fantasy.ToolCallContent{
					ToolCallID: "readme-1",
					ToolName:   "read_file",
					Input:      `{"path":"README.md"}`,
				},
			},
			FinishReason: fantasy.FinishReasonToolCalls,
		}, nil
	case 2:
		if !promptContainsToolResultText(call.Prompt, "kaguya") {
			m.t.Errorf("second Generate: expected tool message with ToolResultPart containing %q", "kaguya")
		}
		return &fantasy.Response{
			Content: fantasy.ResponseContent{
				fantasy.TextContent{Text: "README 已读取：kaguya"},
			},
			FinishReason: fantasy.FinishReasonStop,
		}, nil
	default:
		return nil, fmt.Errorf("unexpected model call #%d", n)
	}
}

func (m *readmeFakeModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	return nil, nil
}

func (m *readmeFakeModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, nil
}

func (m *readmeFakeModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, nil
}

func (m *readmeFakeModel) Provider() string { return "test-provider" }
func (m *readmeFakeModel) Model() string    { return "test-model" }

func (m *readmeFakeModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// promptContainsUserText reports whether msgs contains a user message
// whose text part equals text.
func promptContainsUserText(msgs []fantasy.Message, text string) bool {
	for _, msg := range msgs {
		if msg.Role != fantasy.MessageRoleUser {
			continue
		}
		for _, part := range msg.Content {
			if tp, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
				if tp.Text == text {
					return true
				}
			}
		}
	}
	return false
}

// promptContainsToolResultText reports whether msgs contains a tool message
// with a ToolResultPart whose text output contains substr.
func promptContainsToolResultText(msgs []fantasy.Message, substr string) bool {
	for _, msg := range msgs {
		if msg.Role != fantasy.MessageRoleTool {
			continue
		}
		for _, part := range msg.Content {
			tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
			if !ok {
				continue
			}
			out, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](tr.Output)
			if !ok {
				continue
			}
			if strings.Contains(out.Text, substr) {
				return true
			}
		}
	}
	return false
}

// errorAgent returns a fixed error from Generate.
type errorAgent struct{ err error }

func (a *errorAgent) Generate(context.Context, fantasy.AgentCall) (*fantasy.AgentResult, error) {
	return nil, a.err
}

func (a *errorAgent) Stream(context.Context, fantasy.AgentStreamCall) (*fantasy.AgentResult, error) {
	return nil, nil
}

// okAgent returns a minimal successful AgentResult with one step.
type okAgent struct{}

func (a *okAgent) Generate(context.Context, fantasy.AgentCall) (*fantasy.AgentResult, error) {
	return &fantasy.AgentResult{
		Steps: []fantasy.StepResult{
			{
				Response: fantasy.Response{
					Content:      fantasy.ResponseContent{fantasy.TextContent{Text: "ok"}},
					FinishReason: fantasy.FinishReasonStop,
				},
				Messages: []fantasy.Message{
					{
						Role: fantasy.MessageRoleAssistant,
						Content: []fantasy.MessagePart{
							fantasy.TextPart{Text: "ok"},
						},
					},
				},
			},
		},
		Response: fantasy.Response{
			Content:      fantasy.ResponseContent{fantasy.TextContent{Text: "ok"}},
			FinishReason: fantasy.FinishReasonStop,
		},
	}, nil
}

func (a *okAgent) Stream(context.Context, fantasy.AgentStreamCall) (*fantasy.AgentResult, error) {
	return nil, nil
}

// errorStore returns configurable errors from Load and/or Append.
type errorStore struct {
	loadErr   error
	appendErr error
}

func (s *errorStore) Load(context.Context, string) ([]fantasy.Message, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return nil, nil
}

func (s *errorStore) Append(context.Context, string, ...fantasy.Message) error {
	return s.appendErr
}

func TestServiceGenerate(t *testing.T) {
	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve project root: %v", err)
	}

	sentinel := errors.New("sentinel error")

	tests := []struct {
		name             string
		useRuntime       bool
		req              agent.Request
		agent            fantasy.Agent
		store            conversation.Store
		wantErr          bool
		wantErrContains  string
		wantErrIs        error
		wantTextContains string
		wantModelCalls   int
	}{
		{
			name:             "readme tool call flow",
			useRuntime:       true,
			req:              agent.Request{ConversationID: "conv-1", MessageID: "msg-1", Prompt: "请读取 README.md"},
			wantErr:          false,
			wantTextContains: "kaguya",
			wantModelCalls:   2,
		},
		{
			name:            "empty conversation id",
			req:             agent.Request{ConversationID: "", Prompt: "hello"},
			agent:           &okAgent{},
			store:           conversation.NewMemoryStore(),
			wantErr:         true,
			wantErrContains: "conversation ID is required",
		},
		{
			name:            "empty prompt",
			req:             agent.Request{ConversationID: "conv-1", Prompt: ""},
			agent:           &okAgent{},
			store:           conversation.NewMemoryStore(),
			wantErr:         true,
			wantErrContains: "prompt is required",
		},
		{
			name:            "agent generate error",
			req:             agent.Request{ConversationID: "conv-1", Prompt: "hello"},
			agent:           &errorAgent{err: sentinel},
			store:           conversation.NewMemoryStore(),
			wantErr:         true,
			wantErrContains: "generate response: " + sentinel.Error(),
			wantErrIs:       sentinel,
		},
		{
			name:            "store load error",
			req:             agent.Request{ConversationID: "conv-1", Prompt: "hello"},
			agent:           &okAgent{},
			store:           &errorStore{loadErr: sentinel},
			wantErr:         true,
			wantErrContains: "load conversation: " + sentinel.Error(),
			wantErrIs:       sentinel,
		},
		{
			name:            "store append error",
			req:             agent.Request{ConversationID: "conv-1", Prompt: "hello"},
			agent:           &okAgent{},
			store:           &errorStore{appendErr: sentinel},
			wantErr:         true,
			wantErrContains: "store messages: " + sentinel.Error(),
			wantErrIs:       sentinel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				agentInst fantasy.Agent
				model     *readmeFakeModel
				store     conversation.Store
				memStore  *conversation.MemoryStore
			)

			if tt.useRuntime {
				model = &readmeFakeModel{t: t, userPrompt: tt.req.Prompt}
				safeFS, err := files.NewSafeFS(files.Config{RootDir: projectRoot})
				if err != nil {
					t.Fatalf("newSafeFS: %v", err)
				}
				agentInst, err = runtime.New(runtime.Config{
					Model:    model,
					Files:    safeFS,
					Recorder: usage.NewMemoryRecorder(),
				})
				if err != nil {
					t.Fatalf("runtime.New: %v", err)
				}
				memStore = conversation.NewMemoryStore()
				store = memStore
			} else {
				agentInst = tt.agent
				store = tt.store
			}

			svc, err := agent.NewService(agentInst, store)
			if err != nil {
				t.Fatalf("NewService: %v", err)
			}

			result, err := svc.Generate(context.Background(), tt.req)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Fatalf("expected error to contain %q, got %q", tt.wantErrContains, err.Error())
				}
				if tt.wantErrIs != nil && !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("expected error to wrap %v, got %v", tt.wantErrIs, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantTextContains != "" && !strings.Contains(result.Text, tt.wantTextContains) {
				t.Fatalf("expected result text to contain %q, got %q", tt.wantTextContains, result.Text)
			}
			if tt.wantModelCalls > 0 && model != nil {
				if got := model.callCount(); got != tt.wantModelCalls {
					t.Fatalf("expected %d model calls, got %d", tt.wantModelCalls, got)
				}
			}
			if tt.useRuntime && memStore != nil {
				checkServiceHistory(t, memStore, tt.req.ConversationID)
			}
		})
	}
}

// checkServiceHistory verifies the store history for the README tool-call flow:
// user message, assistant tool-call message, tool result message, assistant text message.
func checkServiceHistory(t *testing.T, store *conversation.MemoryStore, conversationID string) {
	t.Helper()
	history, err := store.Load(context.Background(), conversationID)
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	if len(history) != 4 {
		t.Fatalf("expected 4 history messages, got %d", len(history))
	}
	if history[0].Role != fantasy.MessageRoleUser {
		t.Errorf("history[0] role = %q, want %q", history[0].Role, fantasy.MessageRoleUser)
	}
	if history[1].Role != fantasy.MessageRoleAssistant {
		t.Errorf("history[1] role = %q, want %q", history[1].Role, fantasy.MessageRoleAssistant)
	}
	if !messageHasToolCallPart(history[1]) {
		t.Errorf("history[1] expected ToolCallPart")
	}
	if history[2].Role != fantasy.MessageRoleTool {
		t.Errorf("history[2] role = %q, want %q", history[2].Role, fantasy.MessageRoleTool)
	}
	if !messageHasToolResultPart(history[2]) {
		t.Errorf("history[2] expected ToolResultPart")
	}
	if history[3].Role != fantasy.MessageRoleAssistant {
		t.Errorf("history[3] role = %q, want %q", history[3].Role, fantasy.MessageRoleAssistant)
	}
	if !messageHasTextPart(history[3]) {
		t.Errorf("history[3] expected TextPart")
	}
}

func messageHasToolCallPart(msg fantasy.Message) bool {
	for _, part := range msg.Content {
		if _, ok := fantasy.AsMessagePart[fantasy.ToolCallPart](part); ok {
			return true
		}
	}
	return false
}

func messageHasToolResultPart(msg fantasy.Message) bool {
	for _, part := range msg.Content {
		if _, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part); ok {
			return true
		}
	}
	return false
}

func messageHasTextPart(msg fantasy.Message) bool {
	for _, part := range msg.Content {
		if _, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
			return true
		}
	}
	return false
}
