//go:build darwin || linux

package system

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"charm.land/fantasy"
	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamcpserver"
	"github.com/lyonmu/kaguya/internal/global"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

var mcpResource = flag.Bool("mcp-resource", false, "run three real SQLCipher/stdio/HTTP/SSE lifecycle and circuit resource rounds")

type mcpResourceInput struct {
	Block bool `json:"block"`
}

// 只记录受控fixture的事件、PID和目录摘要，不记录工具正文/认证值/环境。
type mcpResourceEvent struct {
	Kind   string `json:"kind"`
	PID    int    `json:"pid,omitempty"`
	PGID   int    `json:"pgid,omitempty"`
	WDHash string `json:"wd_hash,omitempty"`
}

type mcpResourceRemote struct {
	mu       sync.Mutex
	attempts int
	quick    int
	blocked  int
	ended    int
	active   int
	forced   int
	cancels  int
	deletes  int
	process  []mcpResourceEvent
	changed  chan struct{}
	fail     atomic.Bool
	sessions func() int
}

func (r *mcpResourceRemote) event(e mcpResourceEvent) {
	r.mu.Lock()
	switch e.Kind {
	case "start":
		r.attempts++
		r.process = append(r.process, e)
	case "descendant":
		r.process = append(r.process, e)
	case "attempt":
		r.attempts++
	case "quick":
		r.quick++
	case "block":
		r.blocked++
		r.active++
	case "forced_cleanup":
		r.forced++
		r.active--
	case "cancelled":
		r.cancels++
	case "delete":
		r.deletes++
	case "end":
		r.ended++
		r.active--
	}
	r.mu.Unlock()
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

type mcpResourceCounts struct {
	Attempts, Quick, Blocked, Ended, Active, Forced, Cancels, Deletes int
	Processes                                                         []mcpResourceEvent
}

func (r *mcpResourceRemote) counts() mcpResourceCounts {
	r.mu.Lock()
	defer r.mu.Unlock()
	return mcpResourceCounts{r.attempts, r.quick, r.blocked, r.ended, r.active, r.forced, r.cancels, r.deletes, append([]mcpResourceEvent(nil), r.process...)}
}

func mcpResourceServer(report func(mcpResourceEvent), cleanup <-chan struct{}) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "resource", Version: "1"}, &sdk.ServerOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	sdk.AddTool(server, &sdk.Tool{Name: "resource"}, func(ctx context.Context, _ *sdk.CallToolRequest, input mcpResourceInput) (*sdk.CallToolResult, any, error) {
		if !input.Block {
			report(mcpResourceEvent{Kind: "quick"})
			return &sdk.CallToolResult{}, nil, nil
		}
		report(mcpResourceEvent{Kind: "block"})
		select {
		case <-ctx.Done():
			report(mcpResourceEvent{Kind: "end"})
		case <-cleanup:
			report(mcpResourceEvent{Kind: "forced_cleanup"})
			return nil, nil, errors.New("fixture cleanup after failed cancellation assertion")
		}
		return nil, nil, ctx.Err()
	})
	return server
}

func mcpResourceDirectoryHash(dir string) string {
	hash := sha256.Sum256([]byte(dir))
	return hex.EncodeToString(hash[:])
}

// 子进程只运行真正SDK stdio server；父进程通过受控回环observer取得PID。
// 正常全量不启用helper；stdout只供MCP wire使用。
func TestMCPResourceHelperProcess(t *testing.T) {
	if os.Getenv("KAGUYA_MCP_RESOURCE_HELPER") != "1" {
		return
	}
	observerURL := os.Getenv("KAGUYA_MCP_RESOURCE_OBSERVER")
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	report := func(e mcpResourceEvent) {
		data, _ := json.Marshal(e)
		response, err := client.Post(observerURL, "application/json", bytes.NewReader(data))
		if err != nil {
			os.Exit(2)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	group, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		os.Exit(2)
	}
	dir, err := os.Getwd()
	if err != nil {
		os.Exit(2)
	}
	report(mcpResourceEvent{Kind: "start", PID: os.Getpid(), PGID: group, WDHash: mcpResourceDirectoryHash(dir)})
	if os.Getenv("KAGUYA_MCP_RESOURCE_FAIL") == "1" {
		os.Exit(1)
	}
	// 真实同组descendant证明停止不是只杀命令父进程；它没有文件/网络副作用。
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	childGroup, err := syscall.Getpgid(child.Process.Pid)
	if err != nil {
		os.Exit(2)
	}
	report(mcpResourceEvent{Kind: "descendant", PID: child.Process.Pid, PGID: childGroup})
	err = mcpResourceServer(report, nil).Run(context.Background(), &sdk.StdioTransport{})
	_ = child.Process.Kill()
	_ = child.Wait()
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func newMCPResourceRemote(t *testing.T, kind string) (*mcpResourceRemote, *dtosystem.SystemMCPSaveReq) {
	t.Helper()
	remote := &mcpResourceRemote{changed: make(chan struct{}, 1)}
	req := &dtosystem.SystemMCPSaveReq{Name: "resource", Transport: kind, TimeoutSeconds: 5}
	if kind == "stdio" {
		observation := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var event mcpResourceEvent
			if err := json.NewDecoder(io.LimitReader(r.Body, 2048)).Decode(&event); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			remote.event(event)
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(observation.Close)
		req.Command = os.Args[0]
		req.Args = []string{"-test.run=^TestMCPResourceHelperProcess$"}
		directory, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		req.WorkingDirectory = directory
		req.Env = map[string]string{"KAGUYA_MCP_RESOURCE_HELPER": "1", "KAGUYA_MCP_RESOURCE_OBSERVER": observation.URL, "KAGUYA_MCP_RESOURCE_FAIL": "0", "GORACE": "atexit_sleep_ms=0", "KAGUYA_SECRET_KEY": "fake-resource-secret"}
		return remote, req
	}
	cleanup := make(chan struct{})
	server := mcpResourceServer(remote.event, cleanup)
	remote.sessions = func() int {
		n := 0
		for range server.Sessions() {
			n++
		}
		return n
	}
	var handler http.Handler
	if kind == "sse" {
		handler = sdk.NewSSEHandler(func(*http.Request) *sdk.Server { return server }, nil)
	} else {
		handler = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true})
	}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-resource-secret" {
			http.Error(w, "invalid fixture header", http.StatusUnauthorized)
			return
		}
		if remote.fail.Load() && kind == "sse" && r.Method == http.MethodGet {
			remote.event(mcpResourceEvent{Kind: "attempt"})
			http.Error(w, "fake-resource-secret", http.StatusServiceUnavailable)
			return
		}
		if r.Method == http.MethodDelete {
			remote.event(mcpResourceEvent{Kind: "delete"})
		}
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
			if err != nil {
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var message struct {
				Method string
				ID     json.RawMessage
			}
			_ = json.Unmarshal(body, &message)
			// 本fixture声明legacy/stateful HTTP；拒绝新版发现探测，
			// SDK客户端按实际旧端点语义回退initialize，不创建探测孤立session。
			if kind == "streamable-http" && message.Method == "server/discover" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}})
				return
			}
			if message.Method == "notifications/cancelled" {
				remote.event(mcpResourceEvent{Kind: "cancelled"})
			}
			if message.Method == "initialize" {
				remote.event(mcpResourceEvent{Kind: "attempt"})
				if remote.fail.Load() {
					http.Error(w, "fake-resource-secret", http.StatusServiceUnavailable)
					return
				}
			}
		}
		// 不可用端点拒绝所有请求，不能让失败初始化后的无session通知
		// 被SDK测试服务器当成新连接，制造fixture自有的孤立session。
		if remote.fail.Load() {
			http.Error(w, "fake-resource-secret", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	// 只在断言后清理fixture；forced_cleanup不能作为远端取消成功。
	t.Cleanup(func() {
		close(cleanup)
		host.CloseClientConnections()
		for session := range server.Sessions() {
			_ = session.Close()
		}
		host.Close()
	})
	req.URL, req.Headers = host.URL, map[string]string{"Authorization": "Bearer fake-resource-secret"}
	return remote, req
}

func mcpResourceWait(t *testing.T, condition func() bool, changed <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-changed:
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("MCP resource owner did not settle before its bounded deadline")
		}
	}
}

func mcpResourceProcessesGone(counts mcpResourceCounts) bool {
	for _, p := range counts.Processes {
		if !errors.Is(syscall.Kill(p.PID, 0), syscall.ESRCH) || !errors.Is(syscall.Kill(-p.PGID, 0), syscall.ESRCH) {
			return false
		}
	}
	return true
}

type mcpResourceMemory struct {
	Heap       uint64 `json:"heap_bytes"`
	RSS        uint64 `json:"rss_bytes"`
	FD         int    `json:"fds"`
	Goroutines int    `json:"goroutines"`
}

func sampleMCPResource(t *testing.T) mcpResourceMemory {
	t.Helper()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	path := "/dev/fd"
	if runtime.GOOS == "linux" {
		path = "/proc/self/fd"
	}
	fds, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Fatal("cannot sample MCP fixture process RSS")
	}
	rss, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		t.Fatal("invalid MCP fixture process RSS")
	}
	return mcpResourceMemory{m.HeapAlloc, rss * 1024, len(fds), runtime.NumGoroutine()}
}

func mcpResourceTool(t *testing.T) fantasy.AgentTool {
	t.Helper()
	tools := agentmcp.Default.Tools()
	if len(tools) != 1 {
		t.Fatal("expected one live fixture MCP adapter")
	}
	return tools[0]
}

func mcpResourceRun(t *testing.T, tool fantasy.AgentTool, wantError bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := tool.Run(ctx, fantasy.ToolCall{Input: `{"block":false}`})
	if err != nil || result.IsError != wantError {
		t.Fatalf("fixture adapter error state=%t wanted=%t err=%v", result.IsError, wantError, err)
	}
}

func mcpResourceBlock(t *testing.T, remote *mcpResourceRemote, tool fantasy.AgentTool) <-chan bool {
	t.Helper()
	before := remote.counts().Blocked
	done := make(chan bool, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		result, err := tool.Run(ctx, fantasy.ToolCall{Input: `{"block":true}`})
		done <- err == nil && result.IsError
	}()
	mcpResourceWait(t, func() bool { return remote.counts().Blocked == before+1 }, remote.changed)
	return done
}

func mcpResourceJoined(t *testing.T, done <-chan bool) {
	t.Helper()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("stopped MCP call did not return the safe connection error")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("active MCP adapter did not terminate")
	}
}

func runMCPResource(t *testing.T, kind string, peak *mcpResourceMemory, counts *mcpResourceCounts) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.DatabaseConfig{Path: filepath.Join(dir, "mcp.db"), KeyFile: filepath.Join(dir, "key")}
	if err := os.WriteFile(cfg.KeyFile, []byte(strings.Repeat("ab", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := db.InitSQLite(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	oldClient, oldID, oldLogger, oldManager, oldPolicy := db.EntClient, global.Id, global.Logger, agentmcp.Default, DefaultMCPRecovery
	core, logs := observer.New(zap.DebugLevel)
	db.EntClient, global.Id, global.Logger, agentmcp.Default, DefaultMCPRecovery = client, &testIDGenerator{}, zap.New(core), agentmcp.NewManager(), NewMCPRecoveryCoordinator()
	root, cancel := context.WithCancel(context.Background())
	svc := NewSystemSvc(root)
	recoveryDone := make(chan struct{})
	go func() { defer close(recoveryDone); svc.RunMCPRecovery(root, MCPRecoveryOptions{Tick: time.Hour}) }()
	t.Cleanup(func() {
		cancel()
		agentmcp.Default.Close()
		<-recoveryDone
		_ = client.Close()
		db.EntClient, global.Id, global.Logger, agentmcp.Default, DefaultMCPRecovery = oldClient, oldID, oldLogger, oldManager, oldPolicy
	})
	remote, req := newMCPResourceRemote(t, kind)
	defer func() { *counts = remote.counts() }()
	// LIFO cleanup必须先停止连接，再关闭其仍用于取消/通知的fixture端点。
	t.Cleanup(func() { cancel(); agentmcp.Default.Close(); <-recoveryDone })
	observe := func() {
		now := sampleMCPResource(t)
		peak.Heap, peak.RSS, peak.FD, peak.Goroutines = max(peak.Heap, now.Heap), max(peak.RSS, now.RSS), max(peak.FD, now.FD), max(peak.Goroutines, now.Goroutines)
	}
	ctx, stop := context.WithTimeout(root, 30*time.Second)
	defer stop()
	created, err := svc.MCPCreate(ctx, req)
	if err != nil || created.Enabled {
		t.Fatal("create must remain disabled", err)
	}
	if _, err := svc.MCPSetEnabled(ctx, created.ID, true); err != nil {
		t.Fatal(err)
	}
	oldTool := mcpResourceTool(t)
	mcpResourceRun(t, oldTool, false)
	blocked := mcpResourceBlock(t, remote, oldTool)
	observe()
	req.Name = "replacement"
	if _, err := svc.MCPUpdate(ctx, created.ID, req); err != nil {
		t.Fatal(err)
	}
	mcpResourceJoined(t, blocked)
	nextTool := mcpResourceTool(t)
	identity := func(tool fantasy.AgentTool) string {
		return tool.(interface{ PermissionIdentity() string }).PermissionIdentity()
	}
	if identity(oldTool) == identity(nextTool) {
		t.Fatal("replacement reused a stale authorization identity")
	}
	beforeCalls := remote.counts()
	mcpResourceRun(t, oldTool, true)
	if remote.counts().Quick != beforeCalls.Quick || remote.counts().Blocked != beforeCalls.Blocked {
		t.Fatal("stale adapter accessed a replacement connection")
	}
	mcpResourceRun(t, nextTool, false)
	blocked = mcpResourceBlock(t, remote, nextTool)
	if _, err := svc.MCPSetEnabled(ctx, created.ID, false); err != nil {
		t.Fatal(err)
	}
	mcpResourceJoined(t, blocked)
	mcpResourceRun(t, nextTool, true)
	if len(agentmcp.Default.Tools()) != 0 {
		t.Fatal("disabled MCP kept callable tools")
	}
	if kind == "stdio" {
		mcpResourceWait(t, func() bool { return mcpResourceProcessesGone(remote.counts()) }, remote.changed)
	} else {
		mcpResourceWait(t, func() bool { return remote.counts().Active == 0 }, remote.changed)
	}
	observe()
	if _, err := svc.MCPSetEnabled(ctx, created.ID, true); err != nil {
		t.Fatal(err)
	}
	deleteTool := mcpResourceTool(t)
	mcpResourceRun(t, deleteTool, false)
	blocked = mcpResourceBlock(t, remote, deleteTool)
	if err := svc.MCPDelete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	mcpResourceJoined(t, blocked)
	mcpResourceRun(t, deleteTool, true)
	if _, exists := DefaultMCPRecovery.Snapshot(created.ID); exists || len(agentmcp.Default.Tools()) != 0 {
		t.Fatal("delete retained live policy/tools")
	}
	// Seed真实已启用的坏配置；API启用失败本来不会持久enabled=true。
	req.Name = "failure"
	remote.fail.Store(true)
	if kind == "stdio" {
		req.Env["KAGUYA_MCP_RESOURCE_FAIL"] = "1"
	}
	broken, err := client.KaguyaMCPServer.Create().SetName(req.Name).SetTransport(kaguyamcpserver.Transport(req.Transport)).
		SetCommand(req.Command).SetArgs(req.Args).SetEnv(req.Env).SetWorkingDirectory(req.WorkingDirectory).
		SetURL(req.URL).SetHeaders(req.Headers).SetTimeoutSeconds(req.TimeoutSeconds).SetEnabled(true).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	startAttempts := remote.counts().Attempts
	if err := svc.RestoreMCP(ctx); err != nil {
		t.Fatal(err)
	}
	options := MCPRecoveryOptions{BaseDelay: time.Second, MaxDelay: time.Second}
	for attempt := 1; attempt < 5; attempt++ {
		svc.recoverMCPConnections(ctx, time.Now().Add(time.Duration(attempt)*time.Minute), options)
	}
	state, exists := DefaultMCPRecovery.Snapshot(broken.ID)
	if !exists || state.Failures != 5 || !state.Open || state.Connecting || remote.counts().Attempts != startAttempts+5 {
		t.Fatalf("five real transport failures did not open circuit: state=%+v attempts=%d", state, remote.counts().Attempts-startAttempts)
	}
	for i := 1; i <= 8; i++ {
		svc.recoverMCPConnections(ctx, time.Now().Add(time.Duration(i)*time.Hour), options)
	}
	DefaultMCPRecovery = NewMCPRecoveryCoordinator() // 新coordinator从同一持久库恢复，不称新应用进程。
	if err := svc.RestoreMCP(ctx); err != nil {
		t.Fatal(err)
	}
	if remote.counts().Attempts != startAttempts+5 {
		t.Fatal("open circuit or restored coordinator made an automatic sixth attempt")
	}
	fault, _ := installMCPPolicyFault(t)
	fault.Store(true)
	if _, err := svc.MCPReconnect(ctx, broken.ID); !errors.Is(err, ErrMCPRecoveryPersistence) {
		t.Fatal("manual cycle storage fault was not closed", err)
	}
	fault.Store(false)
	for i := 1; i <= 3; i++ {
		svc.recoverMCPConnections(ctx, time.Now().Add(time.Duration(i)*time.Hour), options)
	}
	if remote.counts().Attempts != startAttempts+5 {
		t.Fatal("cleared injected fault silently released persistence latch")
	}
	if _, err := svc.MCPReconnect(ctx, broken.ID); !errors.Is(err, ErrMCPConnect) {
		t.Fatal("explicit persisted cycle did not make the sixth diagnostic attempt", err)
	}
	if remote.counts().Attempts != startAttempts+6 {
		t.Fatal("manual cycle performed more than one new attempt")
	}
	observe()
	remote.fail.Store(false)
	if kind == "stdio" {
		req.Env["KAGUYA_MCP_RESOURCE_FAIL"] = "0"
	}
	if _, err := svc.MCPUpdate(ctx, broken.ID, req); err != nil {
		t.Fatal(err)
	}
	state, _ = DefaultMCPRecovery.Snapshot(broken.ID)
	if state.PersistFail || state.Open || state.Failures != 0 || state.Connecting {
		t.Fatal("confirmed replacement failed to clear circuit", state)
	}
	rootTool := mcpResourceTool(t)
	mcpResourceRun(t, rootTool, false)
	blocked = mcpResourceBlock(t, remote, rootTool)
	observe()
	cancel()
	agentmcp.Default.Close()
	mcpResourceJoined(t, blocked)
	select {
	case <-recoveryDone:
	case <-time.After(4 * time.Second):
		t.Fatal("root supervisor did not join")
	}
	mcpResourceRun(t, rootTool, true)
	if _, err := svc.MCPReconnect(context.Background(), broken.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("root cancellation admitted a reconnect", err)
	}
	t.Logf("root-close observed counts: %+v", remote.counts())
	if remote.sessions != nil {
		t.Logf("root-close server sessions: %d", remote.sessions())
	}
	if kind == "stdio" {
		mcpResourceWait(t, func() bool { return mcpResourceProcessesGone(remote.counts()) }, remote.changed)
		for _, p := range remote.counts().Processes {
			if p.Kind == "start" && (p.PID != p.PGID || p.WDHash != mcpResourceDirectoryHash(req.WorkingDirectory)) {
				t.Fatalf("stdio parent identity mismatch: pid=%d pgid=%d cwd_hash=%s expected_cwd_hash=%s", p.PID, p.PGID, p.WDHash, mcpResourceDirectoryHash(req.WorkingDirectory))
			}
			if p.Kind == "descendant" {
				parentFound := false
				for _, parent := range remote.counts().Processes {
					parentFound = parentFound || parent.Kind == "start" && parent.PID == p.PGID
				}
				if !parentFound {
					t.Fatal("stdio descendant did not belong to a fixture parent group")
				}
			}
		}
	} else {
		mcpResourceWait(t, func() bool {
			return remote.counts().Active == 0 && remote.counts().Ended == 4 && remote.counts().Forced == 0 && remote.sessions() == 0
		}, remote.changed)
	}
	if len(agentmcp.Default.Tools()) != 0 || remote.counts().Quick != 4 || remote.counts().Blocked != 4 {
		t.Fatal("final MCP owners or actual call counts did not settle")
	}
	mcpMutations.mu.Lock()
	locks := len(mcpMutations.locks)
	mcpMutations.mu.Unlock()
	if locks != 0 {
		t.Fatal("service mutation gate retained owners")
	}
	if strings.Contains(fmt.Sprint(logs.All()), "fake-resource-secret") || strings.Contains(fmt.Sprint(logs.All()), "fake-secret") {
		t.Fatal("remote or persistence secrets escaped into application logs")
	}
	*counts = remote.counts()
	observe()
}

func TestMCPThreeTransportResources(t *testing.T) {
	if !*mcpResource {
		t.Skip("run with -mcp-resource using the native SQLCipher wrapper")
	}
	for _, kind := range []string{"stdio", "streamable-http", "sse"} {
		for round := 1; round <= 3; round++ {
			runtime.GC()
			before := sampleMCPResource(t)
			peak, counts := before, mcpResourceCounts{}
			started := time.Now()
			ran := false
			passed := t.Run(fmt.Sprintf("%s_round_%d", kind, round), func(t *testing.T) {
				ran = true
				runMCPResource(t, kind, &peak, &counts)
			})
			if !ran {
				continue
			} // -run筛选未执行的子用例不产生资源结果。
			runtime.GC()
			// fixture所有SDK handler、observer、DB与supervisor清理后核对FD。
			mcpResourceWait(t, func() bool { return sampleMCPResource(t).FD == before.FD }, nil)
			after := sampleMCPResource(t)
			if !passed || after.Heap > before.Heap+(32<<20) {
				t.Error("MCP resources did not complete or retained >32MiB heap")
			}
			data, err := json.Marshal(struct {
				Transport string            `json:"transport"`
				Round     int               `json:"round"`
				ElapsedMS float64           `json:"elapsed_ms"`
				Before    mcpResourceMemory `json:"before"`
				Peak      mcpResourceMemory `json:"observed_peak"`
				After     mcpResourceMemory `json:"after_gc_cleanup"`
				Counts    mcpResourceCounts `json:"counts"`
			}{kind, round, float64(time.Since(started)) / float64(time.Millisecond), before, peak, after, counts})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("MCP_RESOURCE %s", data)
		}
	}
}
