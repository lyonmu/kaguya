package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	"github.com/lyonmu/kaguya/internal/agent/permission"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaapproval"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpGenerationResult struct {
	Response fantasy.ToolResponse
	Err      error
}

// A real approval bound to an old MCP adapter must never authorize its replacement.
// Duplicate call IDs fail closed; a new call with the same name/schema/input needs
// its own grant, bound to the replacement generation.
func TestMCPReplacementCannotReuseOldApproval(t *testing.T) {
	for _, transport := range []string{"stdio", "streamable-http", "sse"} {
		t.Run(transport, func(t *testing.T) {
			ctx, client := setupChatTest(t)
			seedApprovalTurn(t, ctx, client)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			markerDir := t.TempDir()
			var oldCalls, newCalls atomic.Int64
			newRemote := func(marker string, calls *atomic.Int64) *httptest.Server {
				server := newApprovalGenerationServer(markerDir, marker, calls)
				var handler http.Handler
				if transport == "sse" {
					handler = sdk.NewSSEHandler(func(*http.Request) *sdk.Server { return server }, nil)
				} else {
					handler = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true})
				}
				remote := httptest.NewServer(handler)
				t.Cleanup(remote.Close)
				return remote
			}
			oldEndpoint, newEndpoint := "old-marker", "new-marker"
			if transport != "stdio" {
				oldEndpoint, newEndpoint = newRemote("old-marker", &oldCalls).URL, newRemote("new-marker", &newCalls).URL
			}
			manager := agentmcp.NewManager()
			t.Cleanup(manager.Close)
			prepare := func(endpoint string) *agentmcp.Connection {
				config := agentmcp.Config{Name: "approval-generation", Transport: transport, URL: endpoint, TimeoutSeconds: 2}
				if transport == "stdio" {
					config.URL, config.Command, config.WorkingDirectory = "", os.Args[0], markerDir
					config.Args = []string{"-test.run=^TestMCPApprovalGenerationHelperProcess$"}
					config.Env = map[string]string{"KAGUYA_MCP_GENERATION_HELPER": "1", "KAGUYA_MCP_GENERATION_MARKER": endpoint}
				}
				connection, err := agentmcp.PrepareWithLifetime(ctx, ctx, config)
				if err != nil {
					t.Fatal(err)
				}
				return connection
			}
			remoteCalls := func(marker string, calls *atomic.Int64) int64 {
				if transport != "stdio" {
					return calls.Load()
				}
				data, err := os.ReadFile(filepath.Join(markerDir, marker))
				if errors.Is(err, os.ErrNotExist) {
					return 0
				}
				if err != nil {
					t.Fatal("cannot read actual stdio execution count", err)
				}
				return int64(strings.Count(string(data), "executed\n"))
			}
			manager.Replace("same-server-id", prepare(oldEndpoint))
			oldTools := manager.Tools()
			if len(oldTools) != 1 {
				t.Fatal("old real MCP adapter missing")
			}
			oldClass := classifyRegisteredTool(oldTools[0], oldTools[0].Info().Name)
			broker := newApprovalBroker("conv-1", "turn-1", nil)
			var workers sync.WaitGroup
			t.Cleanup(func() {
				cancel()
				broker.Close()
				joined := make(chan struct{})
				go func() { workers.Wait(); close(joined) }()
				select {
				case <-joined:
				case <-time.After(5 * time.Second):
					t.Error("MCP generation approval workers did not drain")
				}
			})
			call := fantasy.ToolCall{ID: "same-call", Name: oldTools[0].Info().Name, Input: `{"path":"same-input"}`}
			start := func(tool fantasy.AgentTool, invocation fantasy.ToolCall) <-chan mcpGenerationResult {
				result := make(chan mcpGenerationResult, 1)
				workers.Add(1)
				go func() {
					defer workers.Done()
					response, err := tool.Run(ctx, invocation)
					result <- mcpGenerationResult{Response: response, Err: err}
				}()
				return result
			}
			await := func(result <-chan mcpGenerationResult) mcpGenerationResult {
				select {
				case response := <-result:
					return response
				case <-ctx.Done():
					t.Fatal("generation approval did not finish", ctx.Err())
					return mcpGenerationResult{}
				}
			}
			oldWrapped := permission.WrapAll(permission.ModeFullApproval, broker, []permission.GatedTool{{Tool: oldTools[0], Class: oldClass}})[0]
			oldDone := start(oldWrapped, call)
			oldID := readApprovalID(t)
			manager.Replace("same-server-id", prepare(newEndpoint))
			newTools := manager.Tools()
			if len(newTools) != 1 || newTools[0].Info().Name != call.Name {
				t.Fatal("replacement changed the same-name fixture")
			}
			newClass := classifyRegisteredTool(newTools[0], newTools[0].Info().Name)
			if oldClass.Identity == newClass.Identity || oldClass.Effect != permission.EffectUnknown || newClass.Effect != permission.EffectUnknown {
				t.Fatal("replacement identity/classification did not retain generation binding")
			}
			if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", oldID, "approve"); err != nil {
				t.Fatal(err)
			}
			oldResult := await(oldDone)
			if oldResult.Err != nil || !oldResult.Response.IsError || remoteCalls("old-marker", &oldCalls) != 0 || remoteCalls("new-marker", &newCalls) != 0 {
				t.Fatal("approved stale adapter executed an old or replacement remote tool")
			}
			oldRow, err := dbApprovalByID(ctx, oldID)
			if err != nil || oldRow.Status != kaguyaapproval.StatusConsumed || oldRow.ExecutionState != kaguyaapproval.ExecutionStateFailed || oldRow.ToolIdentity != oldClass.Identity || oldRow.InputHash != permission.HashInput(call.Input) {
				t.Fatal("stale adapter execution audit lost its original binding", err)
			}
			newWrapped := permission.WrapAll(permission.ModeFullApproval, broker, []permission.GatedTool{{Tool: newTools[0], Class: newClass}})[0]
			duplicate := await(start(newWrapped, call))
			if !ent.IsConstraintError(duplicate.Err) || remoteCalls("new-marker", &newCalls) != 0 {
				t.Fatal("duplicate call ID did not fail closed before remote execution", duplicate.Err)
			}
			if count, err := client.KaguyaApproval.Query().Count(ctx); err != nil || count != 1 {
				t.Fatal("duplicate call ID created a second persisted authorization", err)
			}
			call.ID = "new-call"
			newDone := start(newWrapped, call)
			newID := readApprovalID(t)
			if newID == oldID {
				t.Fatal("new generation reused old approval identity")
			}
			// The idempotent old decision may return its audit status, never a new grant.
			decision, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", oldID, "approve")
			if err != nil || decision.Status != string(kaguyaapproval.StatusConsumed) || decision.ApprovalID != oldID {
				t.Fatal("repeated old decision did not remain a historical audit result", err)
			}
			newRow, err := dbApprovalByID(ctx, newID)
			if err != nil || newRow.Status != kaguyaapproval.StatusPending || newRow.ExecutionState != kaguyaapproval.ExecutionStateNotStarted || newRow.ToolIdentity != newClass.Identity || newRow.ToolCallID != call.ID || newRow.InputHash != oldRow.InputHash || remoteCalls("new-marker", &newCalls) != 0 {
				t.Fatal("old decision authorized the replacement generation", err)
			}
			select {
			case <-newDone:
				t.Fatal("replacement returned before its own approval")
			default:
			}
			if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", newID, "approve"); err != nil {
				t.Fatal(err)
			}
			newResult := await(newDone)
			if newResult.Err != nil || newResult.Response.IsError || remoteCalls("old-marker", &oldCalls) != 0 || remoteCalls("new-marker", &newCalls) != 1 {
				t.Fatal("replacement did not run exactly once after its own approval")
			}
			newRow, err = dbApprovalByID(ctx, newID)
			if err != nil || newRow.Status != kaguyaapproval.StatusConsumed || newRow.ExecutionState != kaguyaapproval.ExecutionStateSucceeded {
				t.Fatal("replacement success did not have an independent consumed audit", err)
			}
			if _, err := os.Stat(filepath.Join(markerDir, "old-marker")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("old remote side effect appeared", err)
			}
			if data, err := os.ReadFile(filepath.Join(markerDir, "new-marker")); err != nil || string(data) != "executed\n" {
				t.Fatal("new remote side effect missing", err)
			}
			workers.Wait()
			broker.Close()
			broker.mu.Lock()
			pending := len(broker.pending)
			broker.mu.Unlock()
			approvalRegistry.Lock()
			retained := len(approvalRegistry.entries)
			approvalRegistry.Unlock()
			if pending != 0 || retained != 0 {
				t.Fatal("generation approvals retained waiter/registry resources")
			}
			count, err := client.KaguyaApproval.Query().Count(ctx)
			if err != nil || count != 2 {
				t.Fatal("generation fixture did not retain exactly two independent audits", err)
			}
		})
	}
}

func newApprovalGenerationServer(markerDir, marker string, calls *atomic.Int64) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "approval-generation", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "write", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, func(_ context.Context, _ *sdk.CallToolRequest, input executionMatrixInput) (*sdk.CallToolResult, any, error) {
		if input.Path != "same-input" {
			return nil, nil, errors.New("unexpected generation fixture input")
		}
		if calls != nil {
			calls.Add(1)
		}
		file, err := os.OpenFile(filepath.Join(markerDir, marker), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, nil, err
		}
		_, err = file.WriteString("executed\n")
		err = errors.Join(err, file.Close())
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "executed"}}}, nil, err
	})
	return server
}

func TestMCPApprovalGenerationHelperProcess(t *testing.T) {
	if os.Getenv("KAGUYA_MCP_GENERATION_HELPER") != "1" {
		t.Skip("stdio generation fixture subprocess only")
	}
	marker := os.Getenv("KAGUYA_MCP_GENERATION_MARKER")
	if marker != "old-marker" && marker != "new-marker" {
		os.Exit(2)
	}
	if err := newApprovalGenerationServer(".", marker, nil).Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
