package system

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/global"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

var mcpTestStorageCause = errors.New("fixture database header/env/url=fake-secret")

func installMCPPolicyFault(t *testing.T) (*atomic.Bool, *atomic.Int64) {
	t.Helper()
	active, calls := &atomic.Bool{}, &atomic.Int64{}
	db.EntClient.KaguyaMCPServer.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			m := mutation.(*ent.KaguyaMCPServerMutation)
			if _, writesPolicy := m.ReconnectFailures(); writesPolicy {
				calls.Add(1)
				if active.Load() {
					return nil, mcpTestStorageCause
				}
			}
			return next.Mutate(ctx, mutation)
		})
	})
	return active, calls
}

func mcpPolicyRow(t *testing.T, ctx context.Context) *ent.KaguyaMCPServer {
	t.Helper()
	row, err := db.EntClient.KaguyaMCPServer.Create().SetName("policy").SetTransport("streamable-http").SetURL("http://127.0.0.1:1/mcp").SetTimeoutSeconds(1).SetEnabled(true).SetReconnectFailures(4).SetReconnectCycle(7).SetReconnectNextAt(time.Now().Add(-time.Second)).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	DefaultMCPRecovery.Load(row)
	return row
}

func assertMCPPolicy(t *testing.T, id string, want mcpPolicySnapshot) {
	t.Helper()
	got, exists := DefaultMCPRecovery.Snapshot(id)
	if !exists || got != want {
		t.Fatalf("confirmed policy got=%+v want=%+v", got, want)
	}
	row, err := db.EntClient.KaguyaMCPServer.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if row.ReconnectCycle != want.Cycle || row.ReconnectFailures != want.Failures || row.ReconnectCircuitOpen != want.Open {
		t.Fatalf("durable policy changed: %+v", row)
	}
	if (row.ReconnectNextAt == nil) != want.NextAt.IsZero() || (row.ReconnectNextAt != nil && !row.ReconnectNextAt.Equal(want.NextAt)) {
		t.Fatal("durable retry changed")
	}
}

func TestMCPPolicyPersistenceFailureKeepsConfirmedState(t *testing.T) {
	for _, kind := range []string{"success", "failure", "manual", "clear"} {
		t.Run(kind, func(t *testing.T) {
			ctx := setupMCPServiceTest(t)
			row := mcpPolicyRow(t, ctx)
			before, _ := DefaultMCPRecovery.Snapshot(row.ID)
			active, calls := installMCPPolicyFault(t)
			active.Store(true)
			core, logs := observer.New(zap.DebugLevel)
			global.Logger = zap.New(core)
			var err error
			switch kind {
			case "success":
				err = DefaultMCPRecovery.RecordSuccess(ctx, row.ID)
			case "failure":
				_, err = DefaultMCPRecovery.RecordFailure(ctx, row.ID, time.Millisecond, time.Second, time.Now())
			case "manual":
				_, err = DefaultMCPRecovery.StartManualCycle(ctx, row.ID)
			case "clear":
				err = DefaultMCPRecovery.Clear(ctx, row.ID)
			}
			if !errors.Is(err, ErrMCPRecoveryPersistence) || !errors.Is(err, mcpTestStorageCause) || strings.Contains(err.Error(), "fake-secret") {
				t.Fatalf("unsafe or unclassified storage error: %v", err)
			}
			before.PersistFail = true
			assertMCPPolicy(t, row.ID, before)
			if DefaultMCPRecovery.Allowed(row.ID, time.Now().Add(time.Hour)) {
				t.Fatal("failed write allowed auto reconnect")
			}
			active.Store(false)
			for range 5 {
				DefaultMCPRecovery.Load(row)
				if err := DefaultMCPRecovery.RecordSuccess(ctx, row.ID); !errors.Is(err, ErrMCPRecoveryPersistence) {
					t.Fatalf("healthy cleared latch: %v", err)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("healthy retried failed persistence: %d", calls.Load())
			}
			assertMCPPolicy(t, row.ID, before)
			if strings.Contains(fmt.Sprint(logs.All()), "fake-secret") {
				t.Fatal("storage cause leaked to logs")
			}
			next, err := DefaultMCPRecovery.StartManualCycle(ctx, row.ID)
			if err != nil || next.Cycle != 8 || next.PersistFail || next.Failures != 0 || next.Open {
				t.Fatalf("explicit cycle failed: %+v %v", next, err)
			}
			assertMCPPolicy(t, row.ID, next)
			if !DefaultMCPRecovery.Allowed(row.ID, time.Now()) {
				t.Fatal("persisted manual cycle remained blocked")
			}
		})
	}
}

func TestMCPMissingPolicyRowCannotClearFaultOrRecreate(t *testing.T) {
	ctx := setupMCPServiceTest(t)
	if err := DefaultMCPRecovery.Clear(ctx, "missing"); !errors.Is(err, ErrMCPRecoveryPersistence) {
		t.Fatalf("missing row treated as saved: %v", err)
	}
	if DefaultMCPRecovery.Allowed("missing", time.Now()) {
		t.Fatal("missing state bypassed fault latch")
	}
	state, exists := DefaultMCPRecovery.Snapshot("missing")
	if !exists || !state.PersistFail {
		t.Fatalf("missing fault snapshot: %+v", state)
	}
	count, err := db.EntClient.KaguyaMCPServer.Query().Count(ctx)
	if err != nil || count != 0 {
		t.Fatal("clear recreated service", count, err)
	}
}

// 在真实 HTTP MCP initialize 边界计数/暂停，不替换生产 Prepare。
func mcpControlledServer(t *testing.T, pause *atomic.Bool, entered chan struct{}, release <-chan struct{}) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "controlled", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{}, nil, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true})
	calls := &atomic.Int64{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var request struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &request)
		if request.Method == "initialize" {
			calls.Add(1)
			if pause != nil && pause.Load() {
				select {
				case entered <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { remote.CloseClientConnections(); remote.Close() })
	return remote, calls
}

func setupOwnedMCP(t *testing.T) (context.Context, *SystemSvc) {
	t.Helper()
	ctx := setupMCPServiceTest(t)
	old := agentmcp.Default
	agentmcp.Default = agentmcp.NewManager()
	root, cancel := context.WithCancel(ctx)
	t.Cleanup(func() { cancel(); agentmcp.Default.Close(); agentmcp.Default = old })
	return ctx, NewSystemSvc(root)
}

func createEnabledMCP(t *testing.T, ctx context.Context, svc *SystemSvc, url string) *dtosystem.SystemMCPResp {
	t.Helper()
	created, err := svc.MCPCreate(ctx, &dtosystem.SystemMCPSaveReq{Name: "owned", Transport: "streamable-http", URL: url, TimeoutSeconds: 2})
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := svc.MCPSetEnabled(ctx, created.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	return enabled
}

func TestMCPHealthyTicksCannotClearStorageFault(t *testing.T) {
	ctx, svc := setupOwnedMCP(t)
	remote, prepares := mcpControlledServer(t, nil, nil, nil)
	row := createEnabledMCP(t, ctx, svc, remote.URL)
	if _, err := DefaultMCPRecovery.RecordFailure(ctx, row.ID, time.Millisecond, time.Second, time.Now()); err != nil {
		t.Fatal(err)
	}
	active, calls := installMCPPolicyFault(t)
	active.Store(true)
	svc.recoverMCPConnections(ctx, time.Now().Add(time.Hour), DefaultMCPRecoveryOptions)
	state, _ := DefaultMCPRecovery.Snapshot(row.ID)
	if !state.PersistFail || state.Failures != 1 {
		t.Fatalf("healthy write failure: %+v", state)
	}
	active.Store(false)
	for range 5 {
		svc.recoverMCPConnections(ctx, time.Now().Add(time.Hour), DefaultMCPRecoveryOptions)
	}
	assertMCPPolicy(t, row.ID, state)
	if calls.Load() != 1 || prepares.Load() != 1 {
		t.Fatal("healthy retried write or Prepare", calls.Load(), prepares.Load())
	}
	detail, err := svc.MCPDetail(ctx, row.ID)
	if err != nil || detail.Phase != "storage_error" || detail.Status.State != "running" || len(agentmcp.Default.Tools()) != 1 {
		t.Fatalf("healthy capability conflated with storage fault: %+v %v", detail, err)
	}
	active.Store(true)
	if _, err := svc.MCPReconnect(ctx, row.ID); !errors.Is(err, ErrMCPRecoveryPersistence) {
		t.Fatal(err)
	}
	if prepares.Load() != 1 {
		t.Fatal("manual Prepare ran before cycle persisted")
	}
	active.Store(false)
	oldAdapter := agentmcp.Default.Tools()[0]
	next, err := svc.MCPReconnect(ctx, row.ID)
	if err != nil || next.Phase != "running" || next.ReconnectCycle != row.ReconnectCycle+1 || prepares.Load() != 2 {
		t.Fatalf("manual did not recover: %+v %v", next, err)
	}
	if result, err := oldAdapter.Run(ctx, fantasy.ToolCall{Input: `{}`}); err != nil || !result.IsError {
		t.Fatal("old adapter silently rerouted")
	}
}

func TestMCPHealthyManualInterleaveKeepsNewCycle(t *testing.T) {
	ctx, svc := setupOwnedMCP(t)
	pause := &atomic.Bool{}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	remote, prepares := mcpControlledServer(t, pause, entered, release)
	row := createEnabledMCP(t, ctx, svc, remote.URL)
	pause.Store(true)
	done := make(chan error, 1)
	go func() { _, err := svc.MCPReconnect(ctx, row.ID); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("manual did not reach Prepare")
	}
	before, _ := DefaultMCPRecovery.Snapshot(row.ID)
	if before.Cycle != row.ReconnectCycle+1 || !before.Connecting {
		t.Fatalf("manual cycle not published before Prepare: %+v", before)
	}
	for range 5 {
		svc.recoverMCPConnections(ctx, time.Now(), DefaultMCPRecoveryOptions)
	}
	after, _ := DefaultMCPRecovery.Snapshot(row.ID)
	if after != before || prepares.Load() != 2 {
		t.Fatal("healthy bypassed service gate", before, after, prepares.Load())
	}
	if _, err := svc.MCPReconnect(ctx, row.ID); !errors.Is(err, ErrMCPReconnectBusy) {
		t.Fatal("parallel manual queued", err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	after, _ = DefaultMCPRecovery.Snapshot(row.ID)
	after.Connecting = false
	assertMCPPolicy(t, row.ID, after)
	if after.Cycle != before.Cycle || after.Failures != 0 {
		t.Fatal("late healthy overwrote manual", after)
	}
}

func TestMCPStopAndDeleteCloseEvenWhenPolicyClearFails(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		t.Run(fmt.Sprint(deleting), func(t *testing.T) {
			ctx, svc := setupOwnedMCP(t)
			remote, prepares := mcpControlledServer(t, nil, nil, nil)
			row := createEnabledMCP(t, ctx, svc, remote.URL)
			old := agentmcp.Default.Tools()[0]
			active, _ := installMCPPolicyFault(t)
			active.Store(true)
			var err error
			if deleting {
				err = svc.MCPDelete(ctx, row.ID)
			} else {
				_, err = svc.MCPSetEnabled(ctx, row.ID, false)
			}
			if !errors.Is(err, ErrMCPRecoveryPersistence) {
				t.Fatal("clear failure hidden", err)
			}
			stored, e := db.EntClient.KaguyaMCPServer.Get(ctx, row.ID)
			if e != nil || stored.Enabled || (deleting && stored.DeletedAt == nil) {
				t.Fatalf("stop not durable: %+v %v", stored, e)
			}
			if len(agentmcp.Default.Tools()) != 0 {
				t.Fatal("failed policy clear retained tools")
			}
			if result, e := old.Run(ctx, fantasy.ToolCall{Input: `{}`}); e != nil || !result.IsError {
				t.Fatal("stopped adapter still usable")
			}
			for range 3 {
				svc.recoverMCPConnections(ctx, time.Now().Add(time.Hour), DefaultMCPRecoveryOptions)
			}
			if prepares.Load() != 1 || DefaultMCPRecovery.Allowed(row.ID, time.Now().Add(time.Hour)) {
				t.Fatal("clear failure resurrected stopped service")
			}
			active.Store(false)
			if deleting {
				if _, err := svc.MCPSetEnabled(ctx, row.ID, true); !errors.Is(err, ErrMCPNotFound) {
					t.Fatal(err)
				}
			} else {
				next, err := svc.MCPSetEnabled(ctx, row.ID, true)
				if err != nil || next.ReconnectCycle != row.ReconnectCycle+1 || next.Phase != "running" {
					t.Fatalf("re-enable not a durable cycle: %+v %v", next, err)
				}
			}
		})
	}
}

func TestMCPConfigurationAndCycleSaveAreAtomic(t *testing.T) {
	ctx, svc := setupOwnedMCP(t)
	remote, prepares := mcpControlledServer(t, nil, nil, nil)
	original := createEnabledMCP(t, ctx, svc, remote.URL)
	active, _ := installMCPPolicyFault(t)
	active.Store(true)
	req := original.Config
	req.Name = "replacement"
	req.Headers = map[string]string{"Authorization": "fake-secret"}
	old := agentmcp.Default.Tools()[0]
	if _, err := svc.MCPUpdate(ctx, original.ID, &req); !errors.Is(err, ErrMCPRecoveryPersistence) {
		t.Fatal(err)
	}
	stored, err := db.EntClient.KaguyaMCPServer.Get(ctx, original.ID)
	if err != nil || stored.Name != original.Name || stored.ReconnectCycle != original.ReconnectCycle || len(stored.Headers) != 0 {
		t.Fatalf("partial config persisted: %+v %v", stored, err)
	}
	if result, err := old.Run(ctx, fantasy.ToolCall{Input: `{}`}); err != nil || result.IsError {
		t.Fatal("old healthy connection lost on rejected update", err)
	}
	if prepares.Load() != 2 {
		t.Fatal("replacement wasn't validated", prepares.Load())
	}
	active.Store(false)
	next, err := svc.MCPUpdate(ctx, original.ID, &req)
	if err != nil || next.Name != req.Name || next.ReconnectCycle != original.ReconnectCycle+1 || next.Phase != "running" {
		t.Fatalf("replacement not committed: %+v %v", next, err)
	}
	if result, err := old.Run(ctx, fantasy.ToolCall{Input: `{}`}); err != nil || !result.IsError {
		t.Fatal("old adapter rerouted after commit")
	}
}

func TestMCPFifthFailureAndRestartUseDurablePolicy(t *testing.T) {
	ctx, svc := setupOwnedMCP(t)
	var attempts atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Method == "initialize" {
			attempts.Add(1)
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer remote.Close()
	row, err := svc.MCPCreate(ctx, &dtosystem.SystemMCPSaveReq{Name: "five", Transport: "streamable-http", URL: remote.URL, TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EntClient.KaguyaMCPServer.UpdateOneID(row.ID).SetEnabled(true).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	options := MCPRecoveryOptions{BaseDelay: time.Millisecond, MaxDelay: time.Second}
	for n := range 10 {
		svc.recoverMCPConnections(ctx, time.Now().Add(time.Duration(n+1)*time.Hour), options)
	}
	state, _ := DefaultMCPRecovery.Snapshot(row.ID)
	if attempts.Load() != 5 || !state.Open || state.Failures != 5 {
		t.Fatalf("fifth failure didn't stop: %d %+v", attempts.Load(), state)
	}
	DefaultMCPRecovery = NewMCPRecoveryCoordinator() // 新进程不继承内存，只恢复真实持久行。
	if err := svc.RestoreMCP(ctx); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		svc.recoverMCPConnections(ctx, time.Now().Add(24*time.Hour), options)
	}
	state, _ = DefaultMCPRecovery.Snapshot(row.ID)
	if attempts.Load() != 5 || !state.Open || state.Failures != 5 {
		t.Fatal("restart bypassed durable circuit", attempts.Load(), state)
	}
}

func TestMCPUnpersistedFifthFailureBlocksWithoutFakeOpen(t *testing.T) {
	ctx, svc := setupOwnedMCP(t)
	row := mcpPolicyRow(t, ctx)
	var attempts atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Method == "initialize" {
			attempts.Add(1)
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer remote.Close()
	if err := db.EntClient.KaguyaMCPServer.UpdateOneID(row.ID).SetURL(remote.URL).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	active, _ := installMCPPolicyFault(t)
	active.Store(true)
	before, _ := DefaultMCPRecovery.Snapshot(row.ID)
	before.PersistFail = true
	for range 5 {
		svc.recoverMCPConnections(ctx, time.Now().Add(time.Hour), DefaultMCPRecoveryOptions)
	}
	assertMCPPolicy(t, row.ID, before)
	if attempts.Load() != 1 {
		t.Fatal("storage failure permitted sixth Prepare", attempts.Load())
	}
}

func TestMCPPublicationRejectsLatePreparedConnection(t *testing.T) {
	for _, kind := range []string{"request_cancel", "root_cancel", "disabled", "deleted", "old_cycle"} {
		t.Run(kind, func(t *testing.T) {
			ctx := setupMCPServiceTest(t)
			oldManager := agentmcp.Default
			agentmcp.Default = agentmcp.NewManager()
			defer func() { agentmcp.Default.Close(); agentmcp.Default = oldManager }()
			remote, _ := mcpControlledServer(t, nil, nil, nil)
			root, cancelRoot := context.WithCancel(ctx)
			defer cancelRoot()
			svc := NewSystemSvc(root)
			row := createEnabledMCP(t, ctx, svc, remote.URL)
			// 候选确实已经完成初始化；失败必须 Close，不能只把它从列表隐藏。
			candidate, err := agentmcp.PrepareWithLifetime(ctx, root, row.Config)
			if err != nil {
				t.Fatal(err)
			}
			defer candidate.Close()
			request, cancelRequest := context.WithCancel(ctx)
			defer cancelRequest()
			release, err := mcpMutations.acquire(ctx, row.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			switch kind {
			case "request_cancel":
				cancelRequest()
			case "root_cancel":
				cancelRoot()
			case "disabled":
				err = db.EntClient.KaguyaMCPServer.UpdateOneID(row.ID).SetEnabled(false).Exec(ctx)
			case "deleted":
				err = db.EntClient.KaguyaMCPServer.UpdateOneID(row.ID).SetDeletedAt(time.Now()).Exec(ctx)
			case "old_cycle":
				err = db.EntClient.KaguyaMCPServer.UpdateOneID(row.ID).SetReconnectCycle(row.ReconnectCycle + 1).Exec(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.publishMCP(request, row.ID, row.ReconnectCycle, candidate); err == nil {
				t.Fatal("late prepared candidate published")
			}
			if candidate.Err() == nil {
				t.Fatal("rejected candidate was not closed")
			}
			if kind != "root_cancel" && len(agentmcp.Default.Tools()) != 1 {
				t.Fatal("rejected candidate replaced old connection")
			}
		})
	}
}

func TestMCPGateCanceledContextHasNoAdmission(t *testing.T) {
	var gate = mcpMutationGate{locks: map[string]*mcpServiceLock{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 100 {
		if release, err := gate.acquire(ctx, "id"); err == nil {
			release()
			t.Fatal("canceled context won ready gate")
		}
	}
	if len(gate.locks) != 0 {
		t.Fatal("canceled waiter retained registry")
	}
}

func TestMCPRequiresExplicitRuntimeLifetime(t *testing.T) {
	ctx := setupMCPServiceTest(t)
	if _, err := (&SystemSvc{}).MCPSetEnabled(ctx, "any", true); !errors.Is(err, ErrMCPLifecycle) {
		t.Fatal("MCP silently used background lifetime", err)
	}
}

func TestMCPManualFailureCannotBeClearedByOldHealthyConnection(t *testing.T) {
	ctx, svc := setupOwnedMCP(t)
	var broken atomic.Bool
	var attempts atomic.Int64
	server := sdk.NewServer(&sdk.Implementation{Name: "manual", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{}, nil, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var request struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &request)
		if request.Method == "initialize" {
			attempts.Add(1)
		}
		if broken.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { remote.CloseClientConnections(); remote.Close() })
	row := createEnabledMCP(t, ctx, svc, remote.URL)
	old := agentmcp.Default.Tools()[0]
	broken.Store(true)
	if _, err := svc.MCPReconnect(ctx, row.ID); !errors.Is(err, ErrMCPConnect) {
		t.Fatal(err)
	}
	before, _ := DefaultMCPRecovery.Snapshot(row.ID)
	if before.Failures != 1 || before.Cycle != row.ReconnectCycle+1 || len(agentmcp.Default.Tools()) != 0 {
		t.Fatalf("manual failure kept old healthy connection: %+v", before)
	}
	for range 3 {
		svc.recoverMCPConnections(ctx, time.Now(), DefaultMCPRecoveryOptions)
	}
	after, _ := DefaultMCPRecovery.Snapshot(row.ID)
	if after != before || attempts.Load() != 2 {
		t.Fatal("old healthy tick cleared first failure", after, attempts.Load())
	}
	broken.Store(false)
	svc.recoverMCPConnections(ctx, time.Now().Add(time.Hour), DefaultMCPRecoveryOptions)
	after, _ = DefaultMCPRecovery.Snapshot(row.ID)
	if after.Cycle != before.Cycle || after.Failures != 0 || attempts.Load() != 3 || len(agentmcp.Default.Tools()) != 1 {
		t.Fatal("auto recovery changed manual cycle", after, attempts.Load())
	}
	if result, err := old.Run(ctx, fantasy.ToolCall{Input: `{}`}); err != nil || !result.IsError {
		t.Fatal("old adapter rerouted")
	}
}

func TestMCPConfigurationCommittedBeforeRequestCancelUsesNewConfigOnRecovery(t *testing.T) {
	ctx, svc := setupOwnedMCP(t)
	remote, prepares := mcpControlledServer(t, nil, nil, nil)
	row := createEnabledMCP(t, ctx, svc, remote.URL)
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	db.EntClient.KaguyaMCPServer.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			value, err := next.Mutate(ctx, m)
			if name, changed := m.(*ent.KaguyaMCPServerMutation).Name(); err == nil && changed && name == "committed" {
				cancel()
			}
			return value, err
		})
	})
	req := row.Config
	req.Name = "committed"
	if _, err := svc.MCPUpdate(request, row.ID, &req); !errors.Is(err, context.Canceled) {
		t.Fatal("publication didn't observe post-commit cancellation", err)
	}
	stored, err := db.EntClient.KaguyaMCPServer.Get(ctx, row.ID)
	if err != nil || stored.Name != req.Name || stored.ReconnectCycle != row.ReconnectCycle+1 {
		t.Fatalf("confirmed commit was lost: %+v %v", stored, err)
	}
	if len(agentmcp.Default.Tools()) != 0 {
		t.Fatal("old configuration remained callable after new config committed")
	}
	svc.recoverMCPConnections(ctx, time.Now(), DefaultMCPRecoveryOptions)
	if prepares.Load() != 3 || len(agentmcp.Default.Tools()) != 1 {
		t.Fatal("new durable config wasn't recovered", prepares.Load())
	}
}

func TestMCPStopWriteFailureLatchesFaultAndKeepsConfirmedConnection(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		t.Run(fmt.Sprint(deleting), func(t *testing.T) {
			ctx, svc := setupOwnedMCP(t)
			remote, prepares := mcpControlledServer(t, nil, nil, nil)
			row := createEnabledMCP(t, ctx, svc, remote.URL)
			db.EntClient.KaguyaMCPServer.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					m := mutation.(*ent.KaguyaMCPServerMutation)
					if enabled, present := m.Enabled(); present && !enabled {
						return nil, mcpTestStorageCause
					}
					return next.Mutate(ctx, mutation)
				})
			})
			var err error
			if deleting {
				err = svc.MCPDelete(ctx, row.ID)
			} else {
				_, err = svc.MCPSetEnabled(ctx, row.ID, false)
			}
			if !errors.Is(err, ErrMCPRecoveryPersistence) {
				t.Fatal("stop write failure wasn't latched", err)
			}
			stored, err := db.EntClient.KaguyaMCPServer.Get(ctx, row.ID)
			if err != nil || !stored.Enabled || stored.DeletedAt != nil {
				t.Fatal("failed stop published fake persistent disable", err)
			}
			detail, err := svc.MCPDetail(ctx, row.ID)
			if err != nil || detail.Status.State != "running" || detail.Phase != "storage_error" || len(agentmcp.Default.Tools()) != 1 {
				t.Fatalf("failed stop lost confirmed healthy connection: %+v %v", detail, err)
			}
			for range 3 {
				svc.recoverMCPConnections(ctx, time.Now(), DefaultMCPRecoveryOptions)
			}
			if prepares.Load() != 1 || DefaultMCPRecovery.Allowed(row.ID, time.Now()) {
				t.Fatal("failed stop didn't block auto recovery")
			}
			if _, err := svc.MCPReconnect(ctx, row.ID); err != nil {
				t.Fatal("explicit cycle couldn't clear stop-write fault", err)
			}
		})
	}
}
