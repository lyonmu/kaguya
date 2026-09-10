package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestChatExecutesMCPAndDropsDisabledTools(t *testing.T) {
	ctx, client := setupChatTest(t)
	previous := agentmcp.Default
	agentmcp.Default = agentmcp.NewManager()
	defer func() { agentmcp.Default.Close(); agentmcp.Default = previous }()
	mcpServer := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	var toolCalls atomic.Int64
	sdk.AddTool(mcpServer, &sdk.Tool{Name: "echo"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		toolCalls.Add(1)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "MCP integration result"}}}, nil, nil
	})
	remote := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return mcpServer }, &sdk.StreamableHTTPOptions{JSONResponse: true}))
	defer remote.Close()
	connection, err := agentmcp.Prepare(ctx, agentmcp.Config{Name: "test", Transport: "streamable-http", URL: remote.URL, TimeoutSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	agentmcp.Default.Replace("test", connection)
	var requests atomic.Int64
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		request := requests.Add(1)
		if request < 3 && (len(body.Tools) != 1 || !strings.HasPrefix(body.Tools[0].Function.Name, "mcp_echo_")) {
			t.Errorf("MCP not registered: %+v", body.Tools)
			http.Error(w, "missing tools", 400)
			return
		}
		if request == 3 && len(body.Tools) != 0 {
			t.Error("disabled MCP remained available")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(delta any, reason any) {
			b, _ := json.Marshal(map[string]any{"id": "test", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		if request == 1 {
			emit(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "mcp-call", "type": "function", "function": map[string]any{"name": body.Tools[0].Function.Name, "arguments": "{}"}}}}, nil)
			emit(map[string]any{}, "tool_calls")
		} else {
			if request == 2 && !strings.Contains(string(body.Messages), "MCP integration result") {
				t.Error("MCP result missing from model history")
			}
			emit(map[string]any{"role": "assistant", "content": "done"}, nil)
			emit(map[string]any{}, "stop")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer modelServer.Close()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("mcp-test").SetBaseURL(modelServer.URL).SetAPIKey("test").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run := func() string {
		frames := make(chan *dtochat.ChatResp)
		go (&AgentSvc{}).Chat(ctx, frames, &dtochat.ChatReq{ModelID: model.ID, Messages: "use tools"})
		id := ""
		done := false
		for frame := range frames {
			if frame.Err != nil {
				t.Fatal(frame.Err)
			}
			if frame.Chat.Flag == dtochat.WSFlagDone {
				done = true
				id = frame.Chat.ID
			}
		}
		if !done {
			t.Fatal("chat did not finish")
		}
		return id
	}
	id := run()
	if toolCalls.Load() != 1 || requests.Load() != 2 {
		t.Fatal("MCP was not called exactly once")
	}
	detail, err := (&AgentSvc{}).ConversationDetail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ToolCalls != 1 {
		t.Fatal("MCP call was not persisted")
	}
	agentmcp.Default.Replace("test", nil)
	run()
	if toolCalls.Load() != 1 || requests.Load() != 3 {
		t.Fatal("disabled MCP used by next turn")
	}
}
