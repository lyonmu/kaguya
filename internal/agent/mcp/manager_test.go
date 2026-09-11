package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoInput struct {
	Text string `json:"text"`
}

func TestSSERejectsCrossOriginEndpoint(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true); w.WriteHeader(400) }))
	defer other.Close()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: endpoint\ndata: %s/messages\n\n", other.URL)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer remote.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := Prepare(ctx, Config{Name: "untrusted endpoint", Transport: "sse", URL: remote.URL, Headers: map[string]string{"Authorization": "Bearer secret"}, TimeoutSeconds: 2})
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("cross-origin SSE endpoint accepted")
	}
	if leaked.Load() {
		t.Fatal("request or credentials sent to another origin")
	}
}

func testServer(started ...chan struct{}) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "Echo text"}, func(ctx context.Context, req *sdk.CallToolRequest, input echoInput) (*sdk.CallToolResult, any, error) {
		if input.Text == "wait" {
			if len(started) > 0 {
				started[0] <- struct{}{}
			}
			<-ctx.Done()
			return nil, nil, ctx.Err()
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: input.Text}}}, nil, nil
	})
	return server
}
func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("KAGUYA_MCP_TEST_HELPER") != "1" {
		return
	}
	if err := testServer().Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
func TestTransportsAndDynamicStop(t *testing.T) {
	for _, kind := range []string{"streamable-http", "sse", "stdio"} {
		t.Run(kind, func(t *testing.T) {
			config := Config{Name: "test", Transport: kind, TimeoutSeconds: 5}
			started := make(chan struct{}, 1)
			if kind == "stdio" {
				config.Command = os.Args[0]
				config.Args = []string{"-test.run=^TestMCPHelperProcess$"}
				config.Env = map[string]string{"KAGUYA_MCP_TEST_HELPER": "1"}
				config.WorkingDirectory = t.TempDir()
			} else {
				var handler http.Handler
				server := testServer(started)
				if kind == "sse" {
					handler = sdk.NewSSEHandler(func(*http.Request) *sdk.Server { return server }, nil)
				} else {
					handler = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true})
				}
				httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer secret" {
						http.Error(w, "unauthorized", http.StatusUnauthorized)
						return
					}
					handler.ServeHTTP(w, r)
				}))
				defer httpServer.Close()
				config.URL = httpServer.URL
				config.Headers = map[string]string{"Authorization": "Bearer secret"}
			}
			connection, err := Prepare(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			manager := NewManager()
			defer manager.Close()
			manager.Replace("one", connection)
			tools := manager.Tools()
			if len(tools) != 1 {
				t.Fatalf("tools=%d", len(tools))
			}
			if status := manager.Status("one"); status.State != "running" || len(status.Tools) != 1 {
				t.Fatalf("status=%+v", status)
			}
			// 初始化请求上下文已经结束，但连接必须继续存活。
			response, err := tools[0].Run(context.Background(), fantasy.ToolCall{Input: `{"text":"hello"}`})
			if err != nil || response.IsError || !strings.Contains(response.Content, "hello") {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			result := make(chan fantasy.ToolResponse, 1)
			go func() {
				r, _ := tools[0].Run(context.Background(), fantasy.ToolCall{Input: `{"text":"wait"}`})
				result <- r
			}()
			if kind != "stdio" {
				select {
				case <-started:
				case <-time.After(2 * time.Second):
					t.Fatal("MCP request did not start")
				}
			}
			manager.Replace("one", nil)
			select {
			case response := <-result:
				if !response.IsError {
					t.Fatal("inflight request should be stopped")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("stop did not cancel request")
			}
			response, err = tools[0].Run(context.Background(), fantasy.ToolCall{Input: `{"text":"after stop"}`})
			if err != nil || !response.IsError || len(manager.Tools()) != 0 {
				t.Fatal("old tool remained active")
			}
			if manager.Status("one").State != "stopped" {
				t.Fatal("incorrect stopped state")
			}
		})
	}
}

func TestNamespaceAndInputSchema(t *testing.T) {
	tool := &sdk.Tool{Name: "same.tool", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}}}
	first, err := toolInfo("a", tool)
	if err != nil {
		t.Fatal(err)
	}
	second, err := toolInfo("b", tool)
	if err != nil {
		t.Fatal(err)
	}
	if first.Name == second.Name || len(first.Name) > 64 || len(first.Required) != 1 || first.Parameters["text"] == nil {
		t.Fatalf("invalid tool conversion: %+v", first)
	}
	tool.InputSchema = map[string]any{"type": "object", "$ref": "#/$defs/Test"}
	if _, err := toolInfo("a", tool); err == nil {
		t.Fatal("unsupported schema silently accepted")
	}
}

func TestConfigValidation(t *testing.T) {
	for _, config := range []Config{
		{Name: " ", Transport: "stdio", Command: "echo", TimeoutSeconds: 1},
		{Name: "test", Transport: "stdio", Command: "echo", WorkingDirectory: "relative", TimeoutSeconds: 1},
		{Name: "test", Transport: "stdio", Command: "echo", Env: map[string]string{"BAD=NAME": "v"}, TimeoutSeconds: 1},
		{Name: "test", Transport: "streamable-http", URL: "file:///tmp/server", TimeoutSeconds: 1},
		{Name: "test", Transport: "streamable-http", URL: "https://user:pass@example.com", TimeoutSeconds: 1},
		{Name: "test", Transport: "sse", URL: "https://example.com", Headers: map[string]string{"Authorization": "bad\r\nInjected: true"}, TimeoutSeconds: 1},
		{Name: "test", Transport: "sse", URL: "https://example.com", Headers: map[string]string{"Mcp-Session-Id": "override"}, TimeoutSeconds: 1},
		{Name: "test", Transport: "sse", URL: "https://example.com", Command: "echo", TimeoutSeconds: 1},
	} {
		if err := config.Validate(); err == nil {
			t.Fatalf("invalid config accepted: transport=%s", config.Transport)
		}
	}
}

func TestTimeoutAndFailedConnect(t *testing.T) {
	server := testServer()
	remote := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true}))
	defer remote.Close()
	config := Config{Name: "test", Transport: "streamable-http", URL: remote.URL, TimeoutSeconds: 1}
	connection, err := Prepare(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager()
	defer manager.Close()
	manager.Replace("id", connection)
	response, err := manager.Tools()[0].Run(context.Background(), fantasy.ToolCall{Input: `{"text":"wait"}`})
	if err != nil || !response.IsError {
		t.Fatalf("timeout response=%+v err=%v", response, err)
	}
	config.Headers = map[string]string{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c, err := Prepare(ctx, config); err == nil {
		c.Close()
		t.Fatal("canceled prepare succeeded")
	}
}
