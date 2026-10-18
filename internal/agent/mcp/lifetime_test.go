package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPrepareWithLifetimeSeparatesRequestFromRoot(t *testing.T) {
	for _, kind := range []string{"stdio", "streamable-http", "sse"} {
		t.Run(kind, func(t *testing.T) {
			root, cancelRoot := context.WithCancel(context.Background())
			defer cancelRoot()
			request, cancelRequest := context.WithCancel(context.Background())
			defer cancelRequest()
			config := Config{Name: "lifetime", Transport: kind, TimeoutSeconds: 2}
			if kind == "stdio" {
				config.Command = os.Args[0]
				config.Args = []string{"-test.run=^TestMCPHelperProcess$"}
				config.Env = map[string]string{"KAGUYA_MCP_TEST_HELPER": "1"}
				config.WorkingDirectory = t.TempDir()
			} else {
				server := testServer()
				var handler http.Handler
				if kind == "sse" {
					handler = sdk.NewSSEHandler(func(*http.Request) *sdk.Server { return server }, nil)
				} else {
					handler = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true})
				}
				remote := httptest.NewServer(handler)
				defer remote.Close()
				config.URL = remote.URL
			}
			c, err := PrepareWithLifetime(request, root, config)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			manager := NewManager()
			defer manager.Close()
			manager.Replace("owned", c)
			cancelRequest()
			if c.Err() != nil {
				t.Fatal("successful connection remained owned by request", c.Err())
			}
			tools := manager.Tools()
			if len(tools) != 1 {
				t.Fatal("missing tools")
			}
			result, err := tools[0].Run(context.Background(), fantasy.ToolCall{Input: `{"text":"request returned"}`})
			if err != nil || result.IsError {
				t.Fatal("request completion canceled successful connection", err, result)
			}
			cancelRoot()
			if !errors.Is(c.Err(), context.Canceled) || len(manager.Tools()) != 0 {
				t.Fatal("root failed to cancel connection", c.Err())
			}
			result, err = tools[0].Run(context.Background(), fantasy.ToolCall{Input: `{"text":"old"}`})
			if err != nil || !result.IsError {
				t.Fatal("old adapter remained callable after root cancellation")
			}
			drained := make(chan struct{})
			go func() { _ = c.session.Wait(); close(drained) }()
			select {
			case <-drained:
			case <-time.After(3 * time.Second):
				t.Fatal("transport/session did not drain after root cancellation")
			}
		})
	}
}

func TestPrepareWithLifetimeCancelsInitialization(t *testing.T) {
	for _, kind := range []string{"request", "root", "before_request", "before_root"} {
		t.Run(kind, func(t *testing.T) {
			root, cancelRoot := context.WithCancel(context.Background())
			defer cancelRoot()
			request, cancelRequest := context.WithCancel(context.Background())
			defer cancelRequest()
			entered := make(chan struct{}, 1)
			var calls atomic.Int64
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case entered <- struct{}{}:
				default:
				}
				<-r.Context().Done()
			}))
			defer remote.Close()
			if kind == "before_root" {
				cancelRoot()
			}
			if kind == "before_request" {
				cancelRequest()
			}
			done := make(chan error, 1)
			go func() {
				c, err := PrepareWithLifetime(request, root, Config{Name: "pending", Transport: "streamable-http", URL: remote.URL, TimeoutSeconds: 15})
				if c != nil {
					c.Close()
				}
				done <- err
			}()
			if kind == "request" || kind == "root" {
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("initialize did not start")
				}
				if kind == "request" {
					cancelRequest()
				} else {
					cancelRoot()
				}
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("initialization cancellation lost: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("initialization ignored cancellation")
			}
			if (kind == "before_root" || kind == "before_request") && calls.Load() != 0 {
				t.Fatal("pre-canceled initialization contacted remote")
			}
		})
	}
}

// root关闭必须通过协议通知合作式服务取消执行，不能只让本地Run返回。
func TestRootCancellationNotifiesRunningHTTPTool(t *testing.T) {
	for _, kind := range []string{"streamable-http", "streamable-http-stateless", "sse"} {
		t.Run(kind, func(t *testing.T) {
			entered, canceled, cleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
			server := sdk.NewServer(&sdk.Implementation{Name: "root-cancel", Version: "1"}, &sdk.ServerOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			sdk.AddTool(server, &sdk.Tool{Name: "block"}, func(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
				close(entered)
				select {
				case <-ctx.Done():
					close(canceled)
					return nil, nil, ctx.Err()
				case <-cleanup:
					return nil, nil, errors.New("fixture cleanup after assertion")
				}
			})
			var handler http.Handler
			if kind == "sse" {
				handler = sdk.NewSSEHandler(func(*http.Request) *sdk.Server { return server }, nil)
			} else {
				handler = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true, Stateless: kind == "streamable-http-stateless", PropagateRequestCancellation: kind == "streamable-http-stateless"})
			}
			// 明确模拟legacy stateful endpoint，SDK新发现探测应回退initialize。
			if kind == "streamable-http" {
				legacy := handler
				handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost {
						body, err := io.ReadAll(io.LimitReader(r.Body, 4097))
						if err != nil || len(body) > 4096 {
							http.Error(w, "invalid fixture input", http.StatusBadRequest)
							return
						}
						r.Body = io.NopCloser(bytes.NewReader(body))
						var message struct {
							Method string
							ID     json.RawMessage
						}
						if json.Unmarshal(body, &message) != nil {
							http.Error(w, "invalid fixture input", http.StatusBadRequest)
							return
						}
						if message.Method == "server/discover" {
							w.Header().Set("Content-Type", "application/json")
							_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}})
							return
						}
					}
					legacy.ServeHTTP(w, r)
				})
			}
			remote := httptest.NewServer(handler)
			t.Cleanup(func() {
				close(cleanup)
				remote.CloseClientConnections()
				for session := range server.Sessions() {
					_ = session.Close()
				}
				remote.Close()
			})
			root, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			transport := kind
			if kind == "streamable-http-stateless" {
				transport = "streamable-http"
			}
			c, err := PrepareWithLifetime(context.Background(), root, Config{Name: "root-cancel", Transport: transport, URL: remote.URL, TimeoutSeconds: 10})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(c.Close)
			tools := buildTools("root", c)
			if len(tools) != 1 {
				t.Fatal("missing registered tool")
			}
			joined := make(chan bool, 1)
			go func() {
				response, err := tools[0].Run(context.Background(), fantasy.ToolCall{Input: `{}`})
				joined <- err != nil || response.IsError
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("remote tool did not start")
			}
			cancel()
			c.Close()
			select {
			case <-canceled:
			case <-time.After(3 * time.Second):
				t.Fatal("root canceled local transport before remote tool cancellation")
			}
			select {
			case failed := <-joined:
				if !failed {
					t.Fatal("canceled tool returned success")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("local tool did not join")
			}
			if !errors.Is(c.Err(), context.Canceled) {
				t.Fatal("root cancellation lost", c.Err())
			}
			deadline := time.NewTimer(3 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			for {
				remaining := 0
				for range server.Sessions() {
					remaining++
				}
				if remaining == 0 {
					break
				}
				select {
				case <-tick.C:
				case <-deadline.C:
					t.Fatal("cooperative server retained closed session")
				}
			}
		})
	}
}
