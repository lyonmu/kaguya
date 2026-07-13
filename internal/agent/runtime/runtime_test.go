package runtime_test

import (
	"context"
	"strings"
	"testing"

	"charm.land/fantasy"

	"github.com/lyonmu/kaguya/internal/agent/runtime"
	"github.com/lyonmu/kaguya/internal/agent/tools/files"
	"github.com/lyonmu/kaguya/internal/agent/usage"
)

type fakeModel struct {
	provider string
	model    string
}

func (m *fakeModel) Generate(context.Context, fantasy.Call) (*fantasy.Response, error) {
	return &fantasy.Response{
		Content:      fantasy.ResponseContent{fantasy.TextContent{Text: "ok"}},
		FinishReason: fantasy.FinishReasonStop,
	}, nil
}

func (m *fakeModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	return nil, nil
}

func (m *fakeModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, nil
}

func (m *fakeModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, nil
}

func (m *fakeModel) Provider() string { return m.provider }
func (m *fakeModel) Model() string    { return m.model }

func newSafeFS(t *testing.T) *files.SafeFS {
	t.Helper()
	safeFS, err := files.NewSafeFS(files.Config{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("newSafeFS: %v", err)
	}
	return safeFS
}

func TestNew(t *testing.T) {
	validModel := &fakeModel{provider: "test-provider", model: "test-model"}

	tests := []struct {
		name       string
		model      fantasy.LanguageModel
		files      *files.SafeFS
		recorder   usage.UsageRecorder
		wantErr    bool
		wantErrMsg string
	}{
		{
			name:       "nil model returns error",
			model:      nil,
			files:      newSafeFS(t),
			recorder:   usage.NewMemoryRecorder(),
			wantErr:    true,
			wantErrMsg: "model is required",
		},
		{
			name:       "nil files returns error",
			model:      validModel,
			files:      nil,
			recorder:   usage.NewMemoryRecorder(),
			wantErr:    true,
			wantErrMsg: "file tools are required",
		},
		{
			name:     "valid config returns non-nil agent",
			model:    validModel,
			files:    newSafeFS(t),
			recorder: usage.NewMemoryRecorder(),
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, err := runtime.New(runtime.Config{
				Model:    tt.model,
				Files:    tt.files,
				Recorder: tt.recorder,
			})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Fatalf("expected error message to contain %q, got %q", tt.wantErrMsg, err.Error())
				}
				if agent != nil {
					t.Fatalf("expected nil agent on error, got non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if agent == nil {
				t.Fatalf("expected non-nil agent, got nil")
			}
		})
	}
}

func TestNew_GenerateRecordsUsage(t *testing.T) {
	model := &fakeModel{provider: "test-provider", model: "test-model"}
	recorder := usage.NewMemoryRecorder()

	agent, err := runtime.New(runtime.Config{
		Model:    model,
		Files:    newSafeFS(t),
		Recorder: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = agent.Generate(context.Background(), fantasy.AgentCall{Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	items := recorder.Items()
	if len(items) != 1 {
		t.Fatalf("expected 1 usage item, got %d: %+v", len(items), items)
	}
	if items[0].Model != model.Model() {
		t.Fatalf("expected model %q, got %q", model.Model(), items[0].Model)
	}
	if items[0].Provider != model.Provider() {
		t.Fatalf("expected provider %q, got %q", model.Provider(), items[0].Provider)
	}
	if items[0].Mode != "generate" {
		t.Fatalf("expected mode %q, got %q", "generate", items[0].Mode)
	}
}
