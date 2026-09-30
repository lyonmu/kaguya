package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamcpserver"
	"github.com/lyonmu/kaguya/internal/global"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestMCPCRUDLifecycleAndRestore(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	core, logs := observer.New(zap.DebugLevel)
	global.Logger = zap.New(core)
	manager := agentmcp.Default
	agentmcp.Default = agentmcp.NewManager()
	defer func() { agentmcp.Default.Close(); agentmcp.Default = manager }()
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{}, nil, nil
	})
	remote := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true}))
	defer remote.Close()
	svc := &SystemSvc{}
	req := &dtosystem.SystemMCPSaveReq{Name: "MCP", Transport: "streamable-http", URL: remote.URL, TimeoutSeconds: 2, Headers: map[string]string{"Authorization": "secret"}}
	created, err := svc.MCPCreate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if created.Enabled || created.Status.State != "stopped" {
		t.Fatal("new config should be disabled")
	}
	if _, err := svc.MCPCreate(ctx, req); !errors.Is(err, ErrMCPDuplicate) {
		t.Fatalf("duplicate err=%v", err)
	}
	page, err := svc.MCPPage(ctx, &dtosystem.SystemMCPPageReq{Page: 1, PageSize: 10, Name: "MC"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Items[0].Headers != nil {
		t.Fatalf("invalid list or leaked headers: %+v", page)
	}
	enabled, err := svc.MCPSetEnabled(ctx, created.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled || enabled.Status.State != "running" || len(agentmcp.Default.Tools()) != 1 {
		t.Fatalf("enabled=%+v", enabled)
	}
	oldTools := agentmcp.Default.Tools()
	req.Name = "Updated"
	if _, err := svc.MCPUpdate(ctx, created.ID, req); err != nil {
		t.Fatal(err)
	}
	if len(oldTools) != 1 || len(agentmcp.Default.Tools()) != 1 {
		t.Fatal("wrong tools after replacement")
	}
	badRemote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "secret should not leak", http.StatusUnauthorized)
	}))
	defer badRemote.Close()
	invalid := *req
	invalid.URL = badRemote.URL + "?token=fake-secret"
	if _, err := svc.MCPUpdate(ctx, created.ID, &invalid); !errors.Is(err, ErrMCPConnect) {
		t.Fatalf("bad update err=%v", err)
	} else {
		var cause *agentmcp.Error
		if !errors.As(err, &cause) || strings.Contains(err.Error(), "secret") {
			t.Fatal("safe cause was lost or leaked")
		}
	}
	detail, err := svc.MCPDetail(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.URL != remote.URL || detail.Status.State != "running" || detail.Headers["Authorization"] != "secret" {
		t.Fatalf("failed edit changed config: %+v", detail)
	}
	agentmcp.Default.Close()
	if err := svc.RestoreMCP(ctx); err != nil {
		t.Fatal(err)
	}
	if agentmcp.Default.Status(created.ID).State != "running" {
		t.Fatal("enabled config not restored")
	}
	stopped, err := svc.MCPSetEnabled(ctx, created.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Enabled || len(agentmcp.Default.Tools()) != 0 {
		t.Fatal("stop failed")
	}
	if _, err := svc.MCPUpdate(ctx, created.ID, &invalid); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MCPSetEnabled(ctx, created.ID, true); !errors.Is(err, ErrMCPConnect) {
		t.Fatal("broken config enabled")
	}
	detail, err = svc.MCPDetail(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Enabled || detail.Status.State != "error" {
		t.Fatal("failed enable persisted true or hid failure")
	}
	if strings.Contains(detail.Status.Message, "secret") {
		t.Fatal("unsafe manager status")
	}
	if err := db.EntClient.KaguyaMCPServer.UpdateOneID(created.ID).SetEnabled(true).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestoreMCP(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MCPSetEnabled(ctx, created.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"validate replacement MCP connection failed", "enable MCP connection failed", "restore MCP connection failed"} {
		entries := logs.FilterMessage(message).All()
		if len(entries) != 1 {
			t.Fatalf("logs for %q: %+v", message, entries)
		}
		fields := entries[0].ContextMap()
		if fields["service_id"] != created.ID || fields["transport"] != "streamable-http" || fields["stage"] != "connect" || fields["reason"] == nil {
			t.Fatalf("missing safe fields: %+v", fields)
		}
	}
	if strings.Contains(fmt.Sprint(logs.All()), "secret") || strings.Contains(fmt.Sprint(logs.All()), badRemote.URL) {
		t.Fatal("raw MCP error leaked into logs")
	}
	if _, err := svc.MCPUpdate(ctx, created.ID, req); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MCPSetEnabled(ctx, created.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.MCPDelete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if len(agentmcp.Default.Tools()) != 0 {
		t.Fatal("delete kept tools")
	}
	if _, err := svc.MCPDetail(ctx, created.ID); !errors.Is(err, ErrMCPNotFound) {
		t.Fatalf("deleted detail err=%v", err)
	}
	if _, err := svc.MCPSetEnabled(ctx, created.ID, true); !errors.Is(err, ErrMCPNotFound) {
		t.Fatal("deleted server enabled")
	}
	row, err := db.EntClient.KaguyaMCPServer.Query().Where(kaguyamcpserver.IDEQ(created.ID)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if row.DeletedAt == nil || row.Enabled || len(row.Headers) != 0 || row.Name != req.Name {
		t.Fatal("deleted row was not cleaned or kept its name")
	}
	if _, err := svc.MCPCreate(ctx, req); err != nil {
		t.Fatalf("recreate deleted name: %v", err)
	}
}

// 不同服务的并发门互相独立：一个服务的长时间操作不能阻塞另一个服务；
// 同一服务的等待者可以被 context 取消，取消后不产生副作用。
func TestMCPMutationGateIsolationAndCancellation(t *testing.T) {
	var gate mcpMutationGate
	gate.locks = map[string]*mcpServiceLock{}

	releaseSlow, err := gate.acquire(context.Background(), "slow")
	if err != nil {
		t.Fatal(err)
	}
	// 另一个服务可以立即获取，不受慢服务持有影响。
	other, err := gate.acquire(context.Background(), "other")
	if err != nil {
		t.Fatalf("other service blocked by slow holder: %v", err)
	}
	other()

	// 同一服务的等待者在请求取消时立即返回。
	waitCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := gate.acquire(waitCtx, "slow"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same-service waiter err=%v", err)
	}
	releaseSlow()

	// 引用计数归零后不残留锁对象。
	gate.mu.Lock()
	remaining := len(gate.locks)
	gate.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("lock map leaked %d entries", remaining)
	}
}

// startMCPRecoveryForTest 启动仅用于测试的重连监督器，返回幂等的停止函数。
func startMCPRecoveryForTest(t *testing.T, svc *SystemSvc, options MCPRecoveryOptions) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.RunMCPRecovery(ctx, options)
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("MCP recovery did not stop after context cancellation")
		}
	}
	return stop
}

// waitMCPState 轮询等待服务进入指定状态。
func waitMCPState(t *testing.T, id, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if agentmcp.Default.Status(id).State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	status := agentmcp.Default.Status(id)
	t.Fatalf("MCP service %s did not reach state %q, got %q (%s)", id, want, status.State, status.Message)
}

// newMCPTestServer 创建一个带 echo 工具的 Streamable HTTP MCP 服务。
func newMCPTestServer(t *testing.T) (*sdk.Server, *httptest.Server) {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{}, nil, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true})
	return server, httptest.NewServer(handler)
}

// TestMCPRecoveryReconnectsEnabledService 验证监督器自动重连已启用但未连接的服务，
// 覆盖应用启动时恢复失败或运行过程中连接断开的场景。
func TestMCPRecoveryReconnectsEnabledService(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	core, logs := observer.New(zap.DebugLevel)
	global.Logger = zap.New(core)
	manager := agentmcp.Default
	agentmcp.Default = agentmcp.NewManager()

	_, remoteServer := newMCPTestServer(t)
	svc := &SystemSvc{}
	created, err := svc.MCPCreate(ctx, &dtosystem.SystemMCPSaveReq{Name: "Recovery", Transport: "streamable-http", URL: remoteServer.URL, TimeoutSeconds: 2})
	if err != nil {
		t.Fatal(err)
	}
	// 模拟“已启用但当前未连接”：直接写库，不经过 MCPSetEnabled。
	if err := db.EntClient.KaguyaMCPServer.UpdateOneID(created.ID).SetEnabled(true).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if agentmcp.Default.Status(created.ID).State == "running" {
		t.Fatal("service unexpectedly connected before recovery")
	}

	stop := startMCPRecoveryForTest(t, svc, MCPRecoveryOptions{Tick: 20 * time.Millisecond, BaseDelay: 10 * time.Millisecond, MaxDelay: time.Second})
	defer func() {
		stop()
		agentmcp.Default.Close()
		remoteServer.Close()
		agentmcp.Default = manager
	}()

	waitMCPState(t, created.ID, "running", 5*time.Second)
	if tools := agentmcp.Default.Tools(); len(tools) != 1 {
		t.Fatalf("recovered connection exposed %d tools, want 1", len(tools))
	}
	if logs.FilterMessage("MCP reconnect attempt failed").Len() != 0 {
		t.Fatal("healthy service should reconnect on first attempt")
	}
}

// TestMCPRecoveryBackoffAndReconnect 验证失败按指数退避调度，服务恢复后自动连接。
func TestMCPRecoveryBackoffAndReconnect(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	core, logs := observer.New(zap.DebugLevel)
	global.Logger = zap.New(core)
	manager := agentmcp.Default
	agentmcp.Default = agentmcp.NewManager()

	var attempts int32
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{}, nil, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true})
	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewBuffer(body))
		// 前两次连接失败，模拟服务尚未就绪；同一 handler 保持会话状态。
		if r.Method == "POST" && bytes.Contains(body, []byte(`"method":"initialize"`)) && atomic.AddInt32(&attempts, 1) < 3 {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))

	svc := &SystemSvc{}
	created, err := svc.MCPCreate(ctx, &dtosystem.SystemMCPSaveReq{Name: "Backoff", Transport: "streamable-http", URL: remoteServer.URL, TimeoutSeconds: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EntClient.KaguyaMCPServer.UpdateOneID(created.ID).SetEnabled(true).Exec(ctx); err != nil {
		t.Fatal(err)
	}

	stop := startMCPRecoveryForTest(t, svc, MCPRecoveryOptions{Tick: 20 * time.Millisecond, BaseDelay: 20 * time.Millisecond, MaxDelay: time.Second})
	defer func() {
		stop()
		agentmcp.Default.Close()
		remoteServer.Close()
		agentmcp.Default = manager
	}()

	waitMCPState(t, created.ID, "running", 5*time.Second)
	if got := atomic.LoadInt32(&attempts); got < 3 {
		t.Fatalf("expected at least 3 connection attempts, got %d", got)
	}

	failLogs := logs.FilterMessage("MCP reconnect attempt failed").All()
	if len(failLogs) < 2 {
		t.Fatalf("expected at least 2 reconnect failure logs, got %d", len(failLogs))
	}
	// 指数退避：第一次失败 20ms，第二次翻倍为 40ms。
	if delay := failLogs[0].ContextMap()["next_retry"]; delay != 20*time.Millisecond {
		t.Errorf("first retry delay = %v, want 20ms", delay)
	}
	if delay := failLogs[1].ContextMap()["next_retry"]; delay != 40*time.Millisecond {
		t.Errorf("second retry delay = %v, want 40ms", delay)
	}
	if recovered := logs.FilterMessage("MCP connection recovered").All(); len(recovered) != 1 {
		t.Fatalf("expected 1 recovery log, got %d", len(recovered))
	}
}

// TestMCPRecoveryStopsAfterDisable 验证用户停用服务后监督器停止重连。
func TestMCPRecoveryStopsAfterDisable(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	core, logs := observer.New(zap.DebugLevel)
	global.Logger = zap.New(core)
	manager := agentmcp.Default
	agentmcp.Default = agentmcp.NewManager()
	defer func() {
		agentmcp.Default.Close()
		agentmcp.Default = manager
	}()

	svc := &SystemSvc{}
	// 指向必定拒绝连接的地址，保证重连失败进入退避。
	created, err := svc.MCPCreate(ctx, &dtosystem.SystemMCPSaveReq{Name: "Disabled", Transport: "streamable-http", URL: "http://127.0.0.1:1/mcp", TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EntClient.KaguyaMCPServer.UpdateOneID(created.ID).SetEnabled(true).Exec(ctx); err != nil {
		t.Fatal(err)
	}

	stop := startMCPRecoveryForTest(t, svc, MCPRecoveryOptions{Tick: 10 * time.Millisecond, BaseDelay: 10 * time.Millisecond, MaxDelay: time.Second})
	stopOnce := false
	defer func() {
		if !stopOnce {
			stop()
		}
	}()

	// 等待至少一次失败，期间监督器持有服务门。
	deadline := time.Now().Add(5 * time.Second)
	for logs.FilterMessage("MCP reconnect attempt failed").Len() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("recovery did not attempt to reconnect")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := svc.MCPSetEnabled(ctx, created.ID, false); err != nil {
		t.Fatal(err)
	}
	// 再观察一段时间，停用后不应出现新的失败日志。
	time.Sleep(200 * time.Millisecond)
	before := logs.FilterMessage("MCP reconnect attempt failed").Len()
	time.Sleep(200 * time.Millisecond)
	if after := logs.FilterMessage("MCP reconnect attempt failed").Len(); after != before {
		t.Fatalf("recovery kept retrying after disable: %d -> %d", before, after)
	}
	stop()
	stopOnce = true

	status := agentmcp.Default.Status(created.ID)
	if status.State == "running" {
		t.Fatalf("disabled service should not be connected: %+v", status)
	}
}

// TestMCPBackoffDelay 验证指数退避延迟计算与上限。
func TestMCPBackoffDelay(t *testing.T) {
	options := MCPRecoveryOptions{BaseDelay: time.Second, MaxDelay: 30 * time.Second}
	for _, tc := range []struct {
		failures int
		want     time.Duration
	}{
		{0, time.Second},
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{5, 16 * time.Second},
		{6, 30 * time.Second},
		{100, 30 * time.Second},
	} {
		if got := mcpBackoffDelay(options, tc.failures); got != tc.want {
			t.Errorf("mcpBackoffDelay(failures=%d) = %v, want %v", tc.failures, got, tc.want)
		}
	}
}

// TestMCPMutationGateTryAcquire 验证非阻塞获取：占用时立即失败，释放后可重新获取且不泄漏。
func TestMCPMutationGateTryAcquire(t *testing.T) {
	var gate mcpMutationGate
	gate.locks = map[string]*mcpServiceLock{}

	release, ok := gate.tryAcquire("svc")
	if !ok {
		t.Fatal("first tryAcquire failed")
	}
	if _, ok := gate.tryAcquire("svc"); ok {
		t.Fatal("tryAcquire must fail while the gate is held")
	}
	release()
	releaseAgain, ok := gate.tryAcquire("svc")
	if !ok {
		t.Fatal("tryAcquire after release failed")
	}
	releaseAgain()

	gate.mu.Lock()
	remaining := len(gate.locks)
	gate.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("lock map leaked %d entries", remaining)
	}
}
