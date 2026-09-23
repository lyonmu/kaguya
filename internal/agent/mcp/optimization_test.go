package mcp

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestPreparedToolsConcurrentRunAndReplacement(t *testing.T) {
	var calls atomic.Int64
	server := sdk.NewServer(&sdk.Implementation{Name: "concurrent", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo"}, func(_ context.Context, _ *sdk.CallToolRequest, in echoInput) (*sdk.CallToolResult, any, error) {
		calls.Add(1)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: in.Text}}}, nil, nil
	})
	remote := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true}))
	defer remote.Close()
	manager := NewManager()
	defer manager.Close()
	config := Config{Name: "test", Transport: "streamable-http", URL: remote.URL, TimeoutSeconds: 5}
	for _, id := range []string{"z", "a"} {
		c, err := Prepare(context.Background(), config)
		if err != nil {
			t.Fatal(err)
		}
		manager.Replace(id, c)
	}
	initial := manager.Tools()
	for i, id := range []string{"a", "z"} {
		hash := sha256.Sum256([]byte(id + "\x00echo"))
		if initial[i].Info().Name != fmt.Sprintf("mcp_echo_%x", hash[:12]) {
			t.Fatal("tool namespace or service order changed")
		}
	}
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			for range 10 {
				tools := manager.Tools()
				for _, tool := range tools {
					result, err := tool.Run(context.Background(), fantasy.ToolCall{Input: `{"text":"hello"}`})
					if err != nil || result.IsError || !strings.Contains(result.Content, "hello") {
						t.Errorf("Run: %+v %v", result, err)
					}
					invalid, err := tool.Run(context.Background(), fantasy.ToolCall{Input: `{"text":1}`})
					if err != nil || !invalid.IsError {
						t.Errorf("invalid Run: %+v %v", invalid, err)
					}
				}
			}
		})
	}
	workers.Wait()
	if calls.Load() != 320 {
		t.Fatalf("invalid input touched server or calls were lost: %d", calls.Load())
	}
	replacement, err := Prepare(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	manager.Replace("a", replacement)
	if old, _ := initial[0].Run(context.Background(), fantasy.ToolCall{Input: `{"text":"old"}`}); !old.IsError {
		t.Fatal("replaced adapter remained active")
	}
	if next, err := manager.Tools()[0].Run(context.Background(), fantasy.ToolCall{Input: `{"text":"new"}`}); err != nil || next.IsError {
		t.Fatal("replacement adapter unavailable")
	}
}

func TestPrepareRetainsDiscoveryOrderAndSkipsOnlyCompileFailures(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}}
	bad := map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string", "pattern": "["}}}
	tools := []*sdk.Tool{{Name: "dup", InputSchema: schema}, {Name: "bad-secret", InputSchema: bad}, {Name: "dup", InputSchema: schema}}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(202)
			return
		}
		if len(request.ID) == 0 {
			w.WriteHeader(202)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var result any = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "test", "version": "1"}}
		if request.Method == "tools/list" {
			result = map[string]any{"tools": tools}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer remote.Close()
	old := global.Logger
	core, logs := observer.New(zap.DebugLevel)
	global.Logger = zap.New(core)
	defer func() { global.Logger = old }()
	c, err := Prepare(context.Background(), Config{Name: "test", Transport: "streamable-http", URL: remote.URL, TimeoutSeconds: 3})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager()
	defer manager.Close()
	manager.Replace("id", c)
	if names := manager.Status("id").Tools; fmt.Sprint(names) != "[dup bad-secret dup]" {
		t.Fatalf("discovery changed: %v", names)
	}
	if got := manager.Tools(); len(got) != 2 || got[0].Info().Name != got[1].Info().Name {
		t.Fatal("compile failure changed duplicate/order behavior")
	}
	entries := logs.FilterMessage("skip MCP tool with uncompiled schema").All()
	if len(entries) != 1 || entries[0].ContextMap()["tool_index"] != int64(1) || strings.Contains(fmt.Sprint(entries), "bad-secret") {
		t.Fatalf("unsafe/incomplete compile diagnostics: %+v", entries)
	}
}

func TestMCPErrorSafeMessageAndCauseClassification(t *testing.T) {
	for _, tc := range []struct {
		cause  error
		reason string
	}{
		{exec.ErrNotFound, "not_found"}, {context.Canceled, "canceled"}, {context.DeadlineExceeded, "timeout"},
		{&net.DNSError{Err: "fake-secret", Name: "fake-secret"}, "dns"}, {x509.UnknownAuthorityError{}, "tls"},
		{&jsonrpc.Error{Code: -32603, Message: "fake-secret"}, "protocol"}, {errors.New("https://host/?token=fake-secret"), "unknown"},
	} {
		cause := fmt.Errorf("command/header/env=fake-secret: %w", tc.cause)
		wrapped := newError("MCP 工具发现失败", "discover", cause)
		if wrapped.Error() != "MCP 工具发现失败" || !errors.Is(wrapped, tc.cause) {
			t.Fatal("safe wrapper lost message or cause")
		}
		var typed *Error
		if !errors.As(wrapped, &typed) {
			t.Fatal("typed error missing")
		}
		if stage, reason := Diagnostic(wrapped); stage != "discover" || reason != tc.reason {
			t.Fatalf("diagnostic=%s/%s want %s", stage, reason, tc.reason)
		}
	}
}
