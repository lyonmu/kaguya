//go:build integration

package agent_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"

	"github.com/lyonmu/kaguya/internal/agent"
	"github.com/lyonmu/kaguya/internal/agent/conversation"
	"github.com/lyonmu/kaguya/internal/agent/runtime"
	"github.com/lyonmu/kaguya/internal/agent/tools/files"
	"github.com/lyonmu/kaguya/internal/agent/usage"
	"github.com/lyonmu/kaguya/internal/config"
)

func TestIntegrationReadREADME(t *testing.T) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Skip("could not find repo root")
	}
	configPath := filepath.Join(repoRoot, "config.yml")

	cfg, err := config.Load(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Skip("config.yml is required for integration test")
		}
		t.Fatalf("%v", err)
	}
	if len(cfg.Providers) == 0 {
		t.Fatal("no providers configured")
	}

	p := cfg.Providers[0]
	modelInfo, err := p.DefaultModel()
	if err != nil {
		t.Fatalf("%v", err)
	}

	ctx := context.Background()
	provider, err := openaicompat.New(
		openaicompat.WithName(p.Name),
		openaicompat.WithBaseURL(p.BaseURL),
		openaicompat.WithAPIKey(p.APIKey),
	)
	if err != nil {
		t.Fatalf("%v", err)
	}

	model, err := provider.LanguageModel(ctx, modelInfo.ID)
	if err != nil {
		t.Fatalf("%v", err)
	}

	safeFS, err := files.NewSafeFS(files.Config{RootDir: repoRoot})
	if err != nil {
		t.Fatalf("%v", err)
	}

	rtAgent, err := runtime.New(runtime.Config{
		Model:        model,
		SystemPrompt: "你是一个简洁的助手。请使用提供的工具完成任务。",
		Files:        safeFS,
		Recorder:     usage.NewMemoryRecorder(),
	})
	if err != nil {
		t.Fatalf("%v", err)
	}

	store := conversation.NewMemoryStore()
	service, err := agent.NewService(rtAgent, store)
	if err != nil {
		t.Fatalf("%v", err)
	}

	result, err := service.Generate(ctx, agent.Request{
		ConversationID: "integration-test",
		MessageID:      "msg-1",
		Prompt:         "使用 read_file 工具读取 README.md，并只回答该文件项目标题。",
	})
	if err != nil {
		t.Fatalf("%v", err)
	}

	if !strings.Contains(result.Text, "kaguya") {
		t.Fatalf("expected response text to contain %q, got %q", "kaguya", result.Text)
	}

	history, err := store.Load(ctx, "integration-test")
	if err != nil {
		t.Fatalf("%v", err)
	}

	var toolCallID string
	for _, msg := range history {
		for _, part := range msg.Content {
			tc, ok := fantasy.AsMessagePart[fantasy.ToolCallPart](part)
			if !ok {
				continue
			}
			if tc.ToolName == "read_file" {
				toolCallID = tc.ToolCallID
			}
		}
	}
	if toolCallID == "" {
		t.Fatal("expected ToolCallPart with ToolName \"read_file\" in store history")
	}

	foundResult := false
	for _, msg := range history {
		for _, part := range msg.Content {
			tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
			if !ok {
				continue
			}
			if tr.ToolCallID == toolCallID {
				foundResult = true
			}
		}
	}
	if !foundResult {
		t.Fatalf("expected ToolResultPart with ToolCallID %q in store history", toolCallID)
	}
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
