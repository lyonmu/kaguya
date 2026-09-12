package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
)

func TestChatUsesInstructionSnapshotAndLiveSystemConfig(t *testing.T) {
	ctx, client := setupChatTest(t)
	type upstreamRequest struct {
		Agent    string
		Model    string
		Stream   bool
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	requests := make(chan upstreamRequest, 4)
	titles := make(chan upstreamRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		req := upstreamRequest{Agent: r.Header.Get("User-Agent"), Model: body.Model, Stream: body.Stream, Messages: body.Messages}
		if body.Stream {
			requests <- req
		} else {
			titles <- req
		}
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"title","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"测试标题"},"finish_reason":"stop"}]}`)
		}
	}))
	defer server.Close()
	p, err := client.KaguyaProviderInfo.Create().SetProviderName("live-config").SetAPIKey("test").SetBaseURL(server.URL + "/v1/chat/completions").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m1, err := client.KaguyaModelsInfo.Create().SetProviderID(p.ID).SetModelName("first").SetModelID("first-api").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := client.KaguyaModelsInfo.Create().SetProviderID(p.ID).SetModelName("second").SetModelID("second-api").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := ""
	agentsPath := filepath.Join(t.TempDir(), "AGENTS.md")
	for i, modelID := range []string{m1.ID, m2.ID, m2.ID, m2.ID} {
		if i == 3 {
			id = "" // A new conversation observes the changed file.
		}
		instructions := fmt.Sprintf("live file instructions-%d", i)
		if err := os.WriteFile(agentsPath, []byte(instructions), 0600); err != nil {
			t.Fatal(err)
		}
		configIndex := min(i, 1)
		config := dtosystem.SystemInfoSaveReq{SystemPrompt: fmt.Sprintf("自定义提示词-%d", configIndex), UserAgent: fmt.Sprintf("Agent/%d", configIndex), DefaultModelID: modelID, TaskModelID: modelID, GlobalAgentsPaths: []string{agentsPath}}
		// Removing the file cannot affect a saved conversation snapshot.
		if i == 2 {
			if err := os.Remove(agentsPath); err != nil {
				t.Fatal(err)
			}
		}
		if i < 2 {
			if _, err := (&servicesystem.SystemSvc{}).InfoUpdate(ctx, &config); err != nil {
				t.Fatal(err)
			}
		}
		ch := make(chan *dtochat.ChatResp)
		go (&AgentSvc{}).Chat(ctx, ch, &dtochat.ChatReq{ID: id, Messages: "hello"})
		done := false
		for frame := range ch {
			if frame.Err != nil {
				t.Fatal(frame.Err)
			}
			if frame.Chat.Flag == dtochat.ChatFlagDone {
				done = true
				id = frame.Chat.ID
			}
		}
		if !done {
			t.Fatal("chat did not finish")
		}
		wantModel := []string{"first-api", "second-api"}[configIndex]
		select {
		case req := <-requests:
			if req.Agent != config.UserAgent || req.Model != wantModel {
				t.Fatalf("request=%+v", req)
			}
			instructionIndex := 0
			if i == 3 {
				instructionIndex = 3
			}
			wantPrompt := servicesystem.ChatSystemPrompt(config.SystemPrompt) + fmt.Sprintf("\n\nGlobal instructions from %s:\nlive file instructions-%d", agentsPath, instructionIndex)
			if len(req.Messages) == 0 || req.Messages[0].Role != "system" || req.Messages[0].Content != wantPrompt {
				t.Fatalf("system prompt=%+v", req.Messages)
			}
			if !strings.Contains(req.Messages[0].Content, "fenced code blocks labeled mermaid") || !strings.Contains(req.Messages[0].Content, "does not host MCP Apps or Excalidraw widgets") {
				t.Fatal("chat request omitted client diagram capabilities")
			}
			if i > 0 {
				for _, msg := range req.Messages {
					if strings.Contains(msg.Content, "自定义提示词-0") {
						t.Fatal("old system prompt persisted in history")
					}
				}
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if i > 0 {
			title := defaultConversationTitle
			if _, err := (&AgentSvc{}).ConversationUpdate(ctx, id, &dtochat.ConversationUpdateReq{Title: &title}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := (&AgentSvc{}).ConversationTitleGenerate(ctx, id); err != nil {
			t.Fatal(err)
		}
		select {
		case req := <-titles:
			if req.Agent != config.UserAgent || req.Model != wantModel {
				t.Fatalf("title request=%+v", req)
			}
			if len(req.Messages) == 0 || req.Messages[0].Content != conversationTitlePrompt {
				t.Fatalf("title lost dedicated prompt: %+v", req.Messages)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
