//go:build darwin || linux

package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
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
	"testing"
	"time"

	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaapproval"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
	"github.com/lyonmu/kaguya/internal/global"
	initialize "github.com/lyonmu/kaguya/internal/init"
	"github.com/lyonmu/kaguya/internal/secret"
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
	"github.com/lyonmu/kaguya/pkg"
)

var runtimeApprovalHTTPChild = flag.Bool("runtime-approval-http-child", false, "isolated runtime approval HTTP fixture subprocess")
var runtimeApprovalHTTPLong = flag.Bool("runtime-approval-http-long-wait", false, "run actual ten-minute HTTP expiry and long pending shutdown combinations")

type runtimeApprovalHTTPID struct{ id atomic.Int64 }

func (g *runtimeApprovalHTTPID) GenID() (int64, error) { return g.id.Add(1), nil }

type runtimeApprovalHTTPResources struct {
	HeapBytes       uint64 `json:"heap_bytes"`
	RSSBytes        int64  `json:"rss_bytes"`
	FDs             int    `json:"fds"`
	Goroutines      int    `json:"goroutines"`
	ApprovalWaiters int    `json:"approval_waiter_stacks"`
}

func sampleRuntimeApprovalHTTP(t *testing.T) runtimeApprovalHTTPResources {
	t.Helper()
	fdPath := "/dev/fd"
	if runtime.GOOS == "linux" {
		fdPath = "/proc/self/fd"
	}
	fds, err := os.ReadDir(fdPath)
	if err != nil {
		t.Fatal("sample runtime FDs", err)
	}
	output, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Fatal("sample runtime RSS", err)
	}
	rss, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	stack := make([]byte, 2<<20)
	n := runtime.Stack(stack, true)
	if n == len(stack) {
		t.Fatal("approval goroutine snapshot truncated")
	}
	return runtimeApprovalHTTPResources{memory.HeapAlloc, rss * 1024, len(fds), runtime.NumGoroutine(), strings.Count(string(stack[:n]), "/internal/service/agent.(*approvalBroker).Await(")}
}

func (r *runtimeApprovalHTTPResources) include(next runtimeApprovalHTTPResources) {
	r.HeapBytes = max(r.HeapBytes, next.HeapBytes)
	r.RSSBytes = max(r.RSSBytes, next.RSSBytes)
	r.FDs = max(r.FDs, next.FDs)
	r.Goroutines = max(r.Goroutines, next.Goroutines)
	r.ApprovalWaiters = max(r.ApprovalWaiters, next.ApprovalWaiters)
}

// Shutdown/secret/default owners are process-global. Each fixture runs in a
// fresh process; no production reset hook or shortened approval TTL is needed.
func TestRuntimeApprovalHTTPShutdown(t *testing.T) {
	if *runtimeApprovalHTTPChild {
		runRuntimeApprovalHTTP(t)
		return
	}
	timeout := 35 * time.Second
	if *runtimeApprovalHTTPLong {
		timeout = 13 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRuntimeApprovalHTTPShutdown$", "-test.v", "-test.timeout="+timeout.String(), "-runtime-approval-http-child", fmt.Sprintf("-runtime-approval-http-long-wait=%t", *runtimeApprovalHTTPLong))
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		t.Fatal("isolated runtime approval HTTP fixture", err)
	}
}

type runtimeApprovalHTTPStream struct {
	name, conversation string
	approval           *dtochat.Approval
	response           *http.Response
	done               chan struct{}
	heartbeats         atomic.Int64
	completions        atomic.Int64
	started            time.Time
}

func runRuntimeApprovalHTTP(t *testing.T) {
	before := sampleRuntimeApprovalHTTP(t)
	peak := before
	home := t.TempDir()
	t.Setenv("HOME", home)
	budget := 25 * time.Second
	if *runtimeApprovalHTTPLong {
		budget = 12 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	var modelRequests atomic.Int64
	modelHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelRequests.Add(1)
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error("decode local model request", err)
			http.Error(w, "invalid fixture", 400)
			return
		}
		name, hasResult := "", false
		for _, message := range request.Messages {
			if message.Role == "tool" {
				hasResult = true
			}
			if message.Role == "user" {
				name, _ = message.Content.(string)
				if parts, ok := message.Content.([]any); ok {
					for _, part := range parts {
						if text, ok := part.(map[string]any); ok && text["type"] == "text" {
							name, _ = text["text"].(string)
						}
					}
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if name != "expiry" && name != "disconnect" && name != "user-stop" && name != "root-stop" && name != "consume-gate" {
			t.Error("local model received an unexpected original question")
			http.Error(w, "invalid fixture input", http.StatusBadRequest)
			return
		}
		delta, reason := map[string]any{"role": "assistant", "content": "done"}, "stop"
		if !hasResult {
			input, _ := json.Marshal(map[string]string{"path": name + "-marker", "content": "unexpected execution"})
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": name + "-call", "type": "function", "function": map[string]any{"name": "write", "arguments": string(input)}}}}
			reason = "tool_calls"
		}
		for _, choice := range []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}, {"index": 0, "delta": map[string]any{}, "finish_reason": reason}} {
			chunk, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "fixture", "choices": []any{choice}})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer modelHost.Close()
	cfg := config.DatabaseConfig{Path: filepath.Join(home, "data.db"), KeyFile: filepath.Join(home, "key")}
	if err := os.WriteFile(cfg.KeyFile, []byte(strings.Repeat("ab", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	seed, err := db.InitSQLite(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	global.Id = &runtimeApprovalHTTPID{}
	if err := initialize.Run(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := seed.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetGlobalAgentsPaths([]string{}).SetModelSyncEnabled(false).SetChatMaxRetries(0).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	key, err := cfg.SQLCipherKeyBytes()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.NewCipher("", key)
	if err != nil {
		t.Fatal(err)
	}
	providerKey, err := cipher.Encrypt("fake-runtime-http-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.KaguyaProviderInfo.Create().SetID("runtime-http-provider").SetProviderName("fixture").SetAPIKey(providerKey).SetBaseURL(modelHost.URL).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.KaguyaModelsInfo.Create().SetID("runtime-http-model").SetProviderID("runtime-http-provider").SetModelName("fixture").SetModelID("fixture").SetRequestPath("/v1/chat/completions").Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.KaguyaProject.Create().SetID("runtime-http-project").SetName("fixture").SetPath(home).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	global.Cfg = config.Cli{Web: true, Host: "127.0.0.1", RouterPrefix: "/kaguya/api", MachineID: 1, DB: cfg, LogInfo: config.LogConfig{Module: "runtime-http-approval", Level: config.InfoLevel, Format: config.JSONFormat}}
	rt := newAppRuntime(ctx)
	defer rt.close()
	if err := rt.init(); err != nil {
		t.Fatal(err)
	}
	engine, err := pkg.NewGin(false, global.Cfg.RouterPrefix)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := rt.newEngine(engine)
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewUnstartedServer(handler)
	host.Config.BaseContext = func(net.Listener) context.Context { return rt.RootContext() }
	host.Start()
	defer host.Close()
	transport := &http.Transport{}
	client := &http.Client{Transport: transport}
	defer transport.CloseIdleConnections()
	request := func(method, path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, host.URL+"/kaguya/api/v1"+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", host.URL)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("runtime fixture HTTP request", err)
		}
		return response
	}
	streams := []*runtimeApprovalHTTPStream{}
	defer func() {
		rt.beginShutdown()
		for _, stream := range streams {
			_ = stream.response.Body.Close()
		}
		host.CloseClientConnections()
		wait, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := serviceagent.WaitActive(wait); err != nil {
			t.Error("cleanup chat owners", err)
		}
	}()
	start := func(name string) *runtimeApprovalHTTPStream {
		t.Helper()
		body, _ := json.Marshal(dtochat.ChatReq{Messages: name, ModelID: "runtime-http-model", ProjectID: "runtime-http-project", PermissionMode: "full_approval"})
		response := request(http.MethodPost, "/chat/sse", string(body))
		stream := &runtimeApprovalHTTPStream{name: name, response: response, done: make(chan struct{}), started: time.Now()}
		streams = append(streams, stream)
		pending := make(chan *dtochat.ChatResp, 1)
		go func() {
			defer close(stream.done)
			scanner := bufio.NewScanner(response.Body)
			scanner.Buffer(make([]byte, 4096), 1<<20)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, ": heartbeat") {
					stream.heartbeats.Add(1)
				}
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var frame struct {
					Code int              `json:"code"`
					Data dtochat.ChatResp `json:"data"`
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
					t.Error("decode runtime SSE", err)
					return
				}
				if frame.Data.Chat.Flag == dtochat.ChatFlagDone {
					stream.completions.Add(1)
				}
				if frame.Data.Approval != nil && frame.Data.Approval.Status == "pending" {
					select {
					case pending <- &frame.Data:
					default:
						t.Error("duplicate approval pending")
					}
				}
			}
		}()
		select {
		case frame := <-pending:
			stream.conversation, stream.approval = frame.Chat.ID, frame.Approval
		case <-ctx.Done():
			t.Fatal("pending not emitted", ctx.Err())
		case <-stream.done:
			t.Fatal("stream closed before approval")
		}
		row, err := db.EntClient.KaguyaApproval.Query().Where(kaguyaapproval.ApprovalIDEQ(stream.approval.ApprovalID)).Only(ctx)
		if err != nil || row.ExpiresAt == nil || row.ExpiresAt.Sub(row.RequestedAt) != 10*time.Minute || row.ExecutionState != kaguyaapproval.ExecutionStateNotStarted {
			t.Fatal("actual production expiry or initial audit changed", err)
		}
		stream.started = row.RequestedAt
		return stream
	}
	var expiry *runtimeApprovalHTTPStream
	if *runtimeApprovalHTTPLong {
		expiry = start("expiry")
		select {
		case <-time.After(15 * time.Second):
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	disconnect, userStop, rootStop, gated := start("disconnect"), start("user-stop"), start("root-stop"), start("consume-gate")
	held := sampleRuntimeApprovalHTTP(t)
	peak.include(held)
	if held.ApprovalWaiters != len(streams) {
		t.Fatal("resource snapshot did not observe every actual approval waiter", held.ApprovalWaiters, len(streams))
	}
	await := func(stream *runtimeApprovalHTTPStream) {
		t.Helper()
		select {
		case <-stream.done:
		case <-time.After(5 * time.Second):
			t.Fatal("HTTP reader did not exit", stream.name)
		}
	}
	assertNoWrites := func() {
		t.Helper()
		for _, stream := range streams {
			if _, err := os.Stat(filepath.Join(home, stream.name+"-marker")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unapproved actual write", stream.name, err)
			}
		}
	}
	if expiry != nil {
		probe := time.NewTicker(30 * time.Second)
		defer probe.Stop()
		for {
			select {
			case <-expiry.done:
				goto expired
			case <-probe.C:
				peak.include(sampleRuntimeApprovalHTTP(t))
				assertNoWrites()
				for _, stream := range []*runtimeApprovalHTTPStream{disconnect, userStop, rootStop, gated} {
					row, err := db.EntClient.KaguyaApproval.Query().Where(kaguyaapproval.ApprovalIDEQ(stream.approval.ApprovalID)).Only(ctx)
					if err != nil || row.Status != kaguyaapproval.StatusPending {
						t.Fatal("waiting approval lost its live deadline", stream.name, err)
					}
				}
				t.Logf("RUNTIME_APPROVAL_HTTP_WAIT elapsed_ms=%.3f pending=4 heartbeats=%d", float64(time.Since(expiry.started))/float64(time.Millisecond), rootStop.heartbeats.Load())
			case <-ctx.Done():
				t.Fatal("real ten-minute expiry did not finish", ctx.Err())
			}
		}
	}
expired:
	if expiry != nil && (time.Since(expiry.started) < 10*time.Minute || expiry.completions.Load() != 1) {
		t.Fatal("real expiry was shortened or failed to complete")
	}
	assertNoWrites()
	if err := disconnect.response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	await(disconnect)
	stopped := request(http.MethodPost, "/chat/conversation/"+userStop.conversation+"/stop", "")
	var stopEnvelope struct {
		Code int `json:"code"`
	}
	err = json.NewDecoder(stopped.Body).Decode(&stopEnvelope)
	stopped.Body.Close()
	if err != nil || stopEnvelope.Code != dtocode.SystemSuccess.Code {
		t.Fatal("real stop API failed", err)
	}
	if err := userStop.response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	await(userStop)
	// Ensure both disconnected turns persist before holding the single DB pool.
	for _, stream := range []*runtimeApprovalHTTPStream{disconnect, userStop} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			row, err := db.EntClient.KaguyaApproval.Query().Where(kaguyaapproval.ApprovalIDEQ(stream.approval.ApprovalID)).Only(ctx)
			if err == nil && row.Status == kaguyaapproval.StatusCanceled {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("disconnect approval did not settle", err)
			}
			time.Sleep(time.Millisecond)
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var gateOnce sync.Once
	db.EntClient.KaguyaApproval.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			status, ok := mutation.(*ent.KaguyaApprovalMutation).Status()
			if ok && status == kaguyaapproval.StatusConsumed {
				gateOnce.Do(func() { close(entered) })
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return next.Mutate(ctx, mutation)
		})
	})
	approved := request(http.MethodPost, "/chat/conversation/"+gated.conversation+"/approvals/"+gated.approval.ApprovalID, `{"decision":"approve"}`)
	var approvedEnvelope struct {
		Code int `json:"code"`
	}
	err = json.NewDecoder(approved.Body).Decode(&approvedEnvelope)
	approved.Body.Close()
	if err != nil || approvedEnvelope.Code != dtocode.SystemSuccess.Code {
		t.Fatal("valid decision did not commit", err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("consumption gate not reached", ctx.Err())
	}
	shutdownAt := time.Now()
	rt.beginShutdown() // Actual production gate stop -> service stop -> root cancellation.
	close(release)
	await(rootStop)
	await(gated)
	shutdownCtx, endShutdown := context.WithTimeout(context.Background(), webShutdownTimeout)
	defer endShutdown()
	if err := host.Config.Shutdown(shutdownCtx); err != nil {
		t.Fatal("HTTP shutdown", err)
	}
	waitCtx, endWait := context.WithTimeout(context.Background(), webDrainBudget)
	defer endWait()
	if err := rt.gate.Wait(waitCtx); err != nil {
		t.Fatal("request gate drain", err)
	}
	if err := serviceagent.WaitActive(waitCtx); err != nil || serviceagent.PendingWork() != 0 {
		t.Fatal("chat owners drain", err)
	}
	assertNoWrites()
	for _, stream := range streams {
		row, err := db.EntClient.KaguyaApproval.Query().Where(kaguyaapproval.ApprovalIDEQ(stream.approval.ApprovalID)).Only(waitCtx)
		want := kaguyaapproval.StatusCanceled
		if stream == expiry {
			want = kaguyaapproval.StatusExpired
		}
		if err != nil || row.Status != want || row.ExecutionState != kaguyaapproval.ExecutionStateNotStarted || row.ConsumedAt != nil {
			t.Fatal("shutdown granted execution or dishonest audit", stream.name, err)
		}
		turn, err := db.EntClient.KaguyaChatTurn.Query().Where(kaguyachatturn.ConversationIDEQ(stream.conversation)).Only(waitCtx)
		wantTurn := kaguyachatturn.StatusInterrupted
		if stream == userStop {
			wantTurn = kaguyachatturn.StatusCanceled
		}
		if stream == expiry {
			wantTurn = kaguyachatturn.StatusCompleted
		}
		if err != nil || turn.Status != wantTurn || turn.UserContent != stream.name {
			t.Fatal("shutdown turn lost lifecycle or user question", stream.name, err)
		}
		pending, err := (&serviceagent.AgentSvc{}).ConversationApprovals(waitCtx, stream.conversation)
		if err != nil || len(pending) != 0 {
			t.Fatal("finished request retained an executable approval", stream.name, err)
		}
		if stream != expiry && stream.completions.Load() != 0 {
			t.Fatal("canceled request emitted done", stream.name)
		}
		if expiry != nil && stream != expiry && (time.Since(stream.started) < 9*time.Minute || stream.heartbeats.Load() == 0) {
			t.Fatal("long pending request was not held through real heartbeats", stream.name)
		}
		t.Logf("RUNTIME_APPROVAL_HTTP_RESULT scenario=%s elapsed_ms=%.3f heartbeats=%d approval=%s execution=%s turn=%s done=%d", stream.name, float64(time.Since(stream.started))/float64(time.Millisecond), stream.heartbeats.Load(), row.Status, row.ExecutionState, turn.Status, stream.completions.Load())
	}
	rt.close()
	for name, done := range map[string]<-chan struct{}{"instructions": rt.instructions.Done(), "titles": rt.titleQueue.Done(), "restore": rt.restoreDone, "recovery": rt.recoveryDone, "catalog": rt.modelSyncDone} {
		select {
		case <-done:
		default:
			t.Fatal("runtime owner retained after close", name)
		}
	}
	if _, err := db.EntClient.KaguyaApproval.Query().Count(context.Background()); err == nil {
		t.Fatal("database remained open after final drain")
	}
	for _, stream := range streams {
		_ = stream.response.Body.Close()
	}
	transport.CloseIdleConnections()
	host.Close()
	modelHost.Close()
	after := sampleRuntimeApprovalHTTP(t)
	deadline := time.Now().Add(3 * time.Second)
	for after.FDs != before.FDs && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		after = sampleRuntimeApprovalHTTP(t)
	}
	peak.include(after)
	if after.FDs != before.FDs || after.ApprovalWaiters != 0 {
		t.Fatal("runtime retained FD or approval waiter after final close", before, after)
	}
	wantRequests := int64(4)
	if expiry != nil {
		wantRequests = 6
	}
	if modelRequests.Load() != wantRequests {
		t.Fatal("shutdown replayed a model/tool request", modelRequests.Load(), wantRequests)
	}
	encoded, err := json.Marshal(struct {
		Long                              bool    `json:"long"`
		ShutdownMS                        float64 `json:"shutdown_ms"`
		PendingWork                       int64   `json:"pending_work"`
		ModelRequests                     int64   `json:"model_requests"`
		Before, Held, ObservedPeak, After runtimeApprovalHTTPResources
	}{*runtimeApprovalHTTPLong, float64(time.Since(shutdownAt)) / float64(time.Millisecond), serviceagent.PendingWork(), modelRequests.Load(), before, held, peak, after})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("RUNTIME_APPROVAL_HTTP_DRAIN %s", encoded)
}
