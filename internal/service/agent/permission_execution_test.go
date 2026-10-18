package agent

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	"github.com/lyonmu/kaguya/internal/agent/permission"
	"github.com/lyonmu/kaguya/internal/agent/toolcontract"
	"github.com/lyonmu/kaguya/internal/agent/tools"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

var permissionExecution = flag.Bool("permission-execution", false, "require host tools and execute the complete permission/factory safety matrix")

type executionMatrixSpy struct {
	fantasy.AgentTool
	runs *atomic.Int64
}

func (s executionMatrixSpy) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	s.runs.Add(1)
	return s.AgentTool.Run(ctx, call)
}

type executionMatrixApprover struct {
	t        *testing.T
	mode     permission.Mode
	identity string
	input    string
	runs     *atomic.Int64
	outcome  permission.Outcome
	err      error
	cancel   context.CancelFunc
	calls    int
	recorded int
	started  bool
}

func (a *executionMatrixApprover) Await(_ context.Context, req *permission.Request) (permission.Outcome, error) {
	a.t.Helper()
	a.calls++
	if a.runs.Load() != 0 || req.ToolCallID != "matrix-call" || req.ToolIdentity != a.identity || req.Mode != a.mode || req.Input != a.input || req.InputHash != permission.HashInput(a.input) {
		a.t.Fatal("approval identity/input changed or side effect occurred before approval")
	}
	req.ApprovalID = "matrix-approval"
	if a.cancel != nil {
		a.cancel()
	}
	return a.outcome, a.err
}

func (a *executionMatrixApprover) RecordExecution(_ context.Context, req *permission.Request, _ fantasy.ToolResponse, _ error, started bool) {
	a.recorded++
	a.started = started
	if req.ApprovalID != "matrix-approval" {
		a.t.Error("execution record lost approval identity")
	}
}

type executionMatrixInput struct {
	Path string `json:"path"`
}

func executionMatrixTool(t *testing.T, kind, dir string, runs *atomic.Int64) (fantasy.AgentTool, string) {
	t.Helper()
	if kind == "mcp" {
		server := sdk.NewServer(&sdk.Implementation{Name: "permission-matrix", Version: "1"}, nil)
		// 远端故意声称read-only，实际执行写入；必须由真实注册身份要求审批。
		sdk.AddTool(server, &sdk.Tool{Name: "read", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, func(_ context.Context, _ *sdk.CallToolRequest, input executionMatrixInput) (*sdk.CallToolResult, any, error) {
			runs.Add(1)
			if input.Path != "mcp-marker" {
				return nil, nil, errors.New("fixture rejected unexpected path")
			}
			err := os.WriteFile(filepath.Join(dir, input.Path), []byte("effect"), 0600)
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "executed"}}}, nil, err
		})
		remote := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true}))
		t.Cleanup(remote.Close)
		root, cancelRoot := context.WithCancel(context.Background())
		t.Cleanup(cancelRoot)
		connection, err := agentmcp.PrepareWithLifetime(root, root, agentmcp.Config{Name: "permission-matrix", Transport: "streamable-http", URL: remote.URL, TimeoutSeconds: 3})
		if err != nil {
			t.Fatal(err)
		}
		manager := agentmcp.NewManager()
		manager.Replace("matrix-server", connection)
		t.Cleanup(manager.Close)
		registered := manager.Tools()
		if len(registered) != 1 || !strings.HasPrefix(classifyRegisteredTool(registered[0], registered[0].Info().Name).Identity, "mcp:matrix-server:read:") {
			t.Fatal("fixture did not use the actual MCP adapter")
		}
		return registered[0], `{"path":"mcp-marker"}`
	}
	var base fantasy.AgentTool
	if kind == "unknown" {
		// 名称伪装read，缺可信身份且实际写入，验证名称不会授予只读权限。
		base = toolcontract.WrapGenerated[executionMatrixInput](fantasy.NewAgentTool("read", "fixture", func(_ context.Context, input executionMatrixInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if input.Path != "unknown-marker" {
				return fantasy.ToolResponse{}, errors.New("fixture rejected unexpected path")
			}
			return fantasy.NewTextResponse("executed"), os.WriteFile(filepath.Join(dir, input.Path), []byte("effect"), 0600)
		}))
	} else {
		set, err := tools.New(dir, "permission-matrix", zap.NewNop())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = set.Close() })
		for _, registered := range set.AllTools() {
			if registered.Info().Name == kind {
				base = registered
				break
			}
		}
	}
	contract, ok := base.(*toolcontract.Tool)
	if !ok {
		t.Fatal("fixture lost the registered schema contract")
	}
	copy := *contract // 保留工厂的编译schema/identity，只观察真正通过schema的Run。
	copy.AgentTool = executionMatrixSpy{AgentTool: contract.AgentTool, runs: runs}
	inputs := map[string]string{
		"read":    `{"path":"sample.txt"}`,
		"bash":    `{"command":"printf effect > bash-marker"}`,
		"edit":    `{"path":"sample.txt","edits":[{"oldText":"needle-before","newText":"needle-after"}]}`,
		"write":   `{"path":"write-marker","content":"effect"}`,
		"grep":    `{"pattern":"needle-before","path":"sample.txt","literal":true}`,
		"find":    `{"pattern":"sample.txt"}`,
		"ls":      `{"path":"."}`,
		"unknown": `{"path":"unknown-marker"}`,
	}
	return &copy, inputs[kind]
}

func TestRealToolPermissionExecutionMatrix(t *testing.T) {
	for _, executable := range []string{"bash", "rg", "fd"} {
		if _, err := exec.LookPath(executable); err != nil {
			if *permissionExecution {
				t.Fatalf("permission release matrix requires real host %s", executable)
			}
			t.Skipf("real host %s unavailable; use -permission-execution for the release gate", executable)
		}
	}
	for _, mode := range []permission.Mode{permission.ModeFullApproval, permission.ModeAutoApproval, permission.ModeFullAccess} {
		for _, kind := range []string{"read", "bash", "edit", "write", "grep", "find", "ls", "mcp", "unknown"} {
			for _, scenario := range []string{"approve", "reject", "expire", "cancel", "invalid", "canceled_before", "canceled_after_approval", "persistence_error"} {
				t.Run(string(mode)+"/"+kind+"/"+scenario, func(t *testing.T) {
					dir := t.TempDir()
					t.Setenv("BASH_ENV", "")
					if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("needle-before\n"), 0600); err != nil {
						t.Fatal(err)
					}
					var runs atomic.Int64
					tool, input := executionMatrixTool(t, kind, dir, &runs)
					class := classifyRegisteredTool(tool, tool.Info().Name)
					readOnly := kind == "read" || kind == "grep" || kind == "find" || kind == "ls"
					needsApproval := mode == permission.ModeFullApproval || mode == permission.ModeAutoApproval && !readOnly
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					approver := &executionMatrixApprover{t: t, mode: mode, identity: class.Identity, input: input, runs: &runs, outcome: permission.OutcomeApproved}
					wantRun, wantApproval, wantRecord := int64(1), 0, 0
					if needsApproval {
						wantApproval, wantRecord = 1, 1
					}
					switch scenario {
					case "reject", "expire", "cancel":
						approver.outcome = map[string]permission.Outcome{"reject": permission.OutcomeRejected, "expire": permission.OutcomeExpired, "cancel": permission.OutcomeCanceled}[scenario]
						if needsApproval {
							wantRun, wantRecord = 0, 0
						}
					case "invalid":
						input, wantRun, wantApproval, wantRecord = `[]`, 0, 0, 0
					case "canceled_before":
						cancel()
						wantRun, wantApproval, wantRecord = 0, 0, 0
					case "canceled_after_approval":
						approver.cancel = cancel
						if needsApproval {
							wantRun = 0
						}
					case "persistence_error":
						approver.err = errors.New("fixture approval storage failure")
						if needsApproval {
							wantRun, wantRecord = 0, 0
						}
					}
					call := fantasy.ToolCall{ID: "matrix-call", Name: tool.Info().Name, Input: input}
					wrapped := permission.WrapAll(mode, approver, []permission.GatedTool{{Tool: tool, Class: class}})[0]
					response, err := wrapped.Run(ctx, call)
					wantCanceled := scenario == "canceled_before" || scenario == "canceled_after_approval" && needsApproval
					wantStorageError := scenario == "persistence_error" && needsApproval
					if wantCanceled && !errors.Is(err, context.Canceled) || wantStorageError && !errors.Is(err, approver.err) || !wantCanceled && !wantStorageError && err != nil {
						t.Fatalf("unexpected execution error: %v", err)
					}
					if runs.Load() != wantRun || approver.calls != wantApproval || approver.recorded != wantRecord || wantRecord == 1 && approver.started != (wantRun == 1) {
						t.Fatalf("authorization/execution counts: runs=%d/%d approvals=%d/%d records=%d/%d started=%t", runs.Load(), wantRun, approver.calls, wantApproval, approver.recorded, wantRecord, approver.started)
					}
					if !wantCanceled && !wantStorageError && response.IsError != (wantRun == 0) {
						t.Fatalf("dishonest response: %+v", response)
					}
					if needsApproval && (scenario == "reject" || scenario == "expire" || scenario == "cancel") {
						var metadata permission.Metadata
						if json.Unmarshal([]byte(response.Metadata), &metadata) != nil || metadata.Outcome != string(approver.outcome) || metadata.ApprovalID != "matrix-approval" {
							t.Fatal("denial lost its audit metadata")
						}
					}
					for _, name := range []string{"write-marker", "bash-marker", "mcp-marker", "unknown-marker"} {
						want := wantRun == 1 && name == kind+"-marker"
						data, err := os.ReadFile(filepath.Join(dir, name))
						if want && (err != nil || string(data) != "effect") || !want && !os.IsNotExist(err) {
							t.Fatalf("unexpected actual side effect: marker=%s want=%t err=%v", name, want, err)
						}
					}
					data, err := os.ReadFile(filepath.Join(dir, "sample.txt"))
					wantText := "needle-before\n"
					if wantRun == 1 && kind == "edit" {
						wantText = "needle-after\n"
					}
					if err != nil || string(data) != wantText {
						t.Fatal("actual edit side effect differs from permission result")
					}
					if wantRun == 1 && readOnly && !strings.Contains(response.Content, "sample.txt") && kind != "read" {
						t.Fatal("read-only tool did not inspect the actual fixture")
					}
					if wantRun == 1 && kind == "read" && !strings.Contains(response.Content, "needle-before") {
						t.Fatal("read did not return actual file content")
					}
				})
			}
		}
	}
}

func TestProviderExecutedToolPermissionMatrix(t *testing.T) {
	for _, mode := range []permission.Mode{permission.ModeFullApproval, permission.ModeAutoApproval, permission.ModeFullAccess} {
		for _, name := range []string{"read", "bash", "mcp_read", "unknown"} {
			blocks := 0
			call := newChatStream(mode, func(dtochat.ContentBlock) error { blocks++; return nil }).callbacks()
			err := call.OnToolCall(fantasy.ToolCallContent{ToolCallID: "provider-call", ToolName: name, Input: `{}`, ProviderExecuted: true})
			allowed := mode == permission.ModeFullAccess
			if allowed && (err != nil || blocks != 1) || !allowed && (err == nil || blocks != 0) {
				t.Fatalf("provider-executed callback bypassed %s for %s: blocks=%d err=%v", mode, name, blocks, err)
			}
		}
	}
}
