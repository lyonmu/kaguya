package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
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
		if input.Text == "environment" {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: os.Getenv("KAGUYA_SECRET_KEY") + "|" + os.Getenv("KAGUYA_MCP_INHERITED") + "|" + os.Getenv("HTTPS_PROXY")}}}, nil, nil
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
			t.Setenv("KAGUYA_SECRET_KEY", "inherited-secret")
			t.Setenv("KAGUYA_MCP_INHERITED", "inherited-value")
			t.Setenv("HTTPS_PROXY", "http://fake-proxy.invalid")
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
			if kind == "stdio" {
				response, err = tools[0].Run(context.Background(), fantasy.ToolCall{Input: `{"text":"environment"}`})
				if err != nil || response.IsError || !strings.Contains(response.Content, "inherited-secret|inherited-value|http://fake-proxy.invalid") {
					t.Fatalf("stdio environment was not inherited: response=%+v err=%v", response, err)
				}
				config.Env["KAGUYA_SECRET_KEY"] = "configured-secret"
				override, err := Prepare(context.Background(), config)
				if err != nil {
					t.Fatal(err)
				}
				defer override.Close()
				response, err = buildTools("override", override)[0].Run(context.Background(), fantasy.ToolCall{Input: `{"text":"environment"}`})
				if err != nil || response.IsError || !strings.Contains(response.Content, "configured-secret|inherited-value|http://fake-proxy.invalid") {
					t.Fatal("stdio override did not preserve inherited variables")
				}
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

// 命令缺失时必须给出可操作提示，而不是笼统的连接失败。
func TestMissingCommandErrorMentionsPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Prepare(ctx, Config{Name: "missing", Transport: "stdio", Command: "kaguya-missing-mcp-command", TimeoutSeconds: 2})
	if err == nil || !strings.Contains(err.Error(), "找不到") || !strings.Contains(err.Error(), "kaguya-missing-mcp-command") {
		t.Fatalf("missing command error=%v", err)
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing command cause was not preserved: %v", err)
	}
	if stage, reason := Diagnostic(err); stage != "connect" || reason != "not_found" {
		t.Fatalf("diagnostic=%s/%s", stage, reason)
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

// 本地执行校验必须使用远端声明的真实约束：缺必填、类型错误、未知字段、
// 数量约束都应在触达远端前失败，合法输入才能转发。
func TestAgentToolValidatesInputLocally(t *testing.T) {
	tool, err := newAgentTool("srv", &Connection{}, &sdk.Tool{Name: "strict.tool", InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "minLength": 2, "maxLength": 8},
			"tags": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}},
		},
		"required":             []any{"name"},
		"additionalProperties": false,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		`{}`,
		`{"name":"a"}`,
		`{"name":123}`,
		`{"name":"ok","extra":1}`,
		`{"name":"ok","tags":[]}`,
	} {
		var args map[string]any
		if err := json.Unmarshal([]byte(invalid), &args); err != nil {
			t.Fatal(err)
		}
		if message := tool.validateInput(args); message == "" {
			t.Fatalf("invalid input accepted: %s", invalid)
		}
	}
	for _, valid := range []string{
		`{"name":"ok"}`,
		`{"name":"ok","tags":["a"]}`,
	} {
		var args map[string]any
		if err := json.Unmarshal([]byte(valid), &args); err != nil {
			t.Fatal(err)
		}
		if message := tool.validateInput(args); message != "" {
			t.Fatalf("valid input rejected: %s -> %s", valid, message)
		}
	}
	// 声明 additionalProperties:false 时未知字段必须在本地被拒绝。
	var extension map[string]any
	if err := json.Unmarshal([]byte(`{"name":"ok","unknown_extension":1}`), &extension); err != nil {
		t.Fatal(err)
	}
	if message := tool.validateInput(extension); message == "" {
		t.Fatal("strict schema accepted unknown field")
	}
	// 宽松 schema（未声明 additionalProperties）允许扩展字段。
	relaxed, err := newAgentTool("srv", &Connection{}, &sdk.Tool{Name: "relaxed", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []any{"name"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if message := relaxed.validateInput(extension); message != "" {
		t.Fatalf("relaxed schema rejected extension: %s", message)
	}
}

// Info 每次返回独立的 schema 树，Provider 规范化嵌套 schema 不得污染后续请求。
func TestToolsReusePreparedValidatorAndKeepAdapterStateIsolated(t *testing.T) {
	remote := &sdk.Tool{Name: "cached", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []string{"value"},
	}}
	prepared, err := prepareTool(remote)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connection := &Connection{ctx: ctx, preparedTools: []preparedTool{prepared}, tools: []*sdk.Tool{remote}}
	manager := NewManager()
	manager.Replace("service", connection)
	first := manager.Tools()[0].(*agentTool)
	second := manager.Tools()[0].(*agentTool)
	if first == second || first.validator != second.validator {
		t.Fatal("Tools did not create isolated adapters backed by the prepared validator")
	}
	first.SetProviderOptions(fantasy.ProviderOptions{})
	if second.ProviderOptions() != nil {
		t.Fatal("provider options leaked between adapters")
	}
	info := first.Info()
	info.Parameters["value"].(map[string]any)["type"] = "integer"
	info.Required[0] = "mutated"
	if first.Info().Required[0] != "value" || second.Info().Required[0] != "value" {
		t.Fatal("Required slice leaked across Info calls or adapters")
	}
	// Once published, even the source discovery object is no longer read by Tools.
	remote.InputSchema = map[string]any{"type": "array"}
	if manager.Tools()[0].Info().Required[0] != "value" {
		t.Fatal("Tools reused mutable discovery metadata")
	}
	if second.Info().Parameters["value"].(map[string]any)["type"] != "string" {
		t.Fatal("nested schema mutation leaked between adapters")
	}
	var failed atomic.Bool
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				if first.validateInput(map[string]any{"value": "ok"}) != "" || second.validateInput(map[string]any{"value": 1}) == "" {
					failed.Store(true)
				}
			}
		}()
	}
	workers.Wait()
	if failed.Load() {
		t.Fatal("shared validator returned inconsistent concurrent results")
	}
}

func BenchmarkBuildToolsWithPreparedSchemas(b *testing.B) {
	for _, count := range []int{1, 64, 256} {
		b.Run(fmt.Sprintf("tools-%d", count), func(b *testing.B) {
			ctx := context.Background()
			connection := &Connection{ctx: ctx}
			for i := range count {
				remote := &sdk.Tool{Name: fmt.Sprintf("tool-%d", i), InputSchema: map[string]any{
					"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}},
				}}
				prepared, err := prepareTool(remote)
				if err != nil {
					b.Fatal(err)
				}
				connection.preparedTools = append(connection.preparedTools, prepared)
				connection.tools = append(connection.tools, remote)
			}
			for _, baseline := range []bool{true, false} {
				name := "cached"
				if baseline {
					name = "baseline"
				}
				b.Run(name, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if baseline {
							tools := make([]fantasy.AgentTool, 0, len(connection.tools))
							for _, remote := range connection.tools {
								tool, err := newAgentTool("service", connection, remote)
								if err != nil {
									b.Fatal(err)
								}
								tools = append(tools, tool)
							}
							if len(tools) != count {
								b.Fatal(len(tools))
							}
						} else if got := buildTools("service", connection); len(got) != count {
							b.Fatal(len(got))
						}
					}
				})
			}
		})
	}
}

func TestAgentToolInfoReturnsFreshSchema(t *testing.T) {
	tool, err := newAgentTool("srv", &Connection{}, &sdk.Tool{Name: "nested", InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"filter": map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	first := tool.Info()
	nested := first.Parameters["filter"].(map[string]any)
	nested["type"] = "mutated"
	nested["properties"].(map[string]any)["key"].(map[string]any)["type"] = "integer"
	second := tool.Info()
	clean := second.Parameters["filter"].(map[string]any)
	if clean["type"] != "object" || clean["properties"].(map[string]any)["key"].(map[string]any)["type"] != "string" {
		t.Fatalf("Info leaked mutable schema: %+v", clean)
	}
}

// 无法保真转换的根关键字仍必须明确拒绝，而不是静默放宽。
func TestAgentToolRejectsUnsupportedSchema(t *testing.T) {
	for _, schema := range []map[string]any{
		{"type": "object", "$ref": "#/$defs/X"},
		{"type": "object", "anyOf": []any{map[string]any{"type": "object"}}},
		{"type": "object", "patternProperties": map[string]any{"^x": map[string]any{"type": "string"}}},
	} {
		if _, err := newAgentTool("srv", &Connection{}, &sdk.Tool{Name: "bad", InputSchema: schema}); err == nil {
			t.Fatalf("unsupported schema accepted: %+v", schema)
		}
	}
}
