package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lyonmu/kaguya/internal/agent/permission"
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
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type secretChatFixture struct {
	ctx    context.Context
	client *ent.Client
	url    string
	http   *http.Client
	logs   *observer.ObservedLogs
}

// Use production SQLCipher initialization and real HTTP handlers. All data,
// keys, model endpoints and errors are synthetic, never the user's database.
func setupSecretChat(t *testing.T) *secretChatFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte(strings.Repeat("ab", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := db.InitSQLite(&config.DatabaseConfig{Path: filepath.Join(dir, "data.db"), KeyFile: key})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error("close isolated SQLCipher client", err)
		}
	})
	oldClient, oldID, oldLogger, oldQueue := db.EntClient, global.Id, global.Logger, serviceagent.DefaultTitleQueue
	core, logs := observer.New(zap.DebugLevel)
	db.EntClient, global.Id, global.Logger = client, &sseAPIID{}, zap.New(core)
	serviceagent.DefaultTitleQueue = nil // Title inference is outside this fixture.
	t.Cleanup(func() {
		db.EntClient, global.Id, global.Logger, serviceagent.DefaultTitleQueue = oldClient, oldID, oldLogger, oldQueue
	})
	if err := initialize.Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetGlobalAgentsPaths([]string{}).SetChatMaxRetries(0).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	api, engine := &ChatApiV1Group{}, gin.New()
	engine.POST("/v1/chat/sse", api.ChatSSE)
	engine.GET("/v1/chat/conversation/:id/approvals", api.ConversationApprovals)
	engine.POST("/v1/chat/conversation/:id/approvals/:approval_id", api.ApprovalDecision)
	engine.GET("/v1/chat/conversation/:id/turns", api.ConversationTurns)
	host := httptest.NewServer(engine)
	transport := &http.Transport{}
	t.Cleanup(func() {
		cancel()
		host.CloseClientConnections()
		transport.CloseIdleConnections()
		host.Close()
		waitSecretChatOwners(t)
	})
	return &secretChatFixture{ctx, client, host.URL, &http.Client{Transport: transport, Timeout: 20 * time.Second}, logs}
}

func waitSecretChatOwners(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for serviceagent.PendingWork() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if serviceagent.PendingWork() != 0 {
		t.Fatal("chat owners did not join before inspecting logs/restoring globals")
	}
}

func (f *secretChatFixture) request(t *testing.T, method, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(f.ctx, method, f.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := f.http.Do(req)
	if err != nil {
		t.Fatal("fixture HTTP request", err)
	}
	return response
}

func (f *secretChatFixture) model(t *testing.T, endpoint, key string) string {
	t.Helper()
	provider, err := f.client.KaguyaProviderInfo.Create().SetProviderName("secret-boundary").SetAPIKey(key).SetBaseURL(endpoint).Save(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := f.client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("fixture").SetModelID("fixture").SetRequestPath("/v1/chat/completions").Save(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return model.ID
}

func requireSecretAbsentFromLogs(t *testing.T, logs *observer.ObservedLogs, markers ...string) {
	t.Helper()
	for _, entry := range logs.All() {
		fields, err := json.Marshal(entry.ContextMap())
		if err != nil {
			t.Fatal("encode observed log fields", err)
		}
		for _, marker := range markers {
			if strings.Contains(entry.Message+string(fields), marker) {
				t.Fatalf("secret crossed application log boundary at %q", entry.Message)
			}
		}
	}
}

func TestChatProviderErrorBodyDoesNotLeakSecrets(t *testing.T) {
	f := setupSecretChat(t)
	const key, prompt, bodySecret = "fake-provider-key-boundary", "fake-user-prompt-boundary", "fake-provider-error-body"
	var requests atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Header.Get("Authorization") != "Bearer "+key || !strings.Contains(string(body), prompt) {
			t.Error("provider must receive the original key/prompt")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": bodySecret + " " + key + " " + prompt, "type": "invalid_request_error"}})
	}))
	t.Cleanup(remote.Close)
	model := f.model(t, remote.URL, key)
	payload, _ := json.Marshal(dtochat.ChatReq{Messages: prompt, ModelID: model, PermissionMode: "full_approval"})
	response := f.request(t, http.MethodPost, "/v1/chat/sse", string(payload))
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || requests.Load() != 1 {
		t.Fatal("provider failure did not complete exactly one request", err)
	}
	for _, marker := range []string{key, prompt, bodySecret} {
		if strings.Contains(string(data), marker) {
			t.Fatal("provider error leaked to generic SSE failure")
		}
	}
	if !strings.Contains(string(data), fmt.Sprintf(`"code":%d`, dtocode.ChatSSEFailure.Code)) || strings.Contains(string(data), `"flag":"done"`) {
		t.Fatal("provider failure became a successful or unclassified SSE completion")
	}
	waitSecretChatOwners(t)
	turn, err := f.client.KaguyaChatTurn.Query().Only(f.ctx)
	if err != nil || turn.Status != kaguyachatturn.StatusFailed || turn.UserContent != prompt {
		t.Fatal("failed turn must preserve its original user message", err)
	}
	if count, err := f.client.KaguyaApproval.Query().Count(f.ctx); err != nil || count != 0 {
		t.Fatal("provider failure registered an executable approval", err)
	}
	if f.logs.FilterMessage("stream chat failed").Len() != 1 {
		t.Fatal("provider failure lost its diagnostic event")
	}
	requireSecretAbsentFromLogs(t, f.logs, key, prompt, bodySecret)
}

func TestChatRetryProviderBodyNeverEntersLogs(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(fmt.Sprintf("recover_%t", recovered), func(t *testing.T) {
			f := setupSecretChat(t)
			if err := f.client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetChatMaxRetries(1).Exec(f.ctx); err != nil {
				t.Fatal(err)
			}
			const key, prompt, bodySecret = "fake-retry-key-boundary", "fake-retry-prompt-boundary", "fake-retry-response-body"
			var calls atomic.Int64
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil || r.Header.Get("Authorization") != "Bearer "+key || !strings.Contains(string(body), prompt) {
					t.Error("retry must send the original provider key and prompt")
				}
				if !recovered || call == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": bodySecret + " " + key + " " + prompt, "type": "rate_limit_error"}})
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"recovered safely\"},\"finish_reason\":null}]}\n\n")
				fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			t.Cleanup(remote.Close)
			model := f.model(t, remote.URL, key)
			payload, err := json.Marshal(dtochat.ChatReq{Messages: prompt, ModelID: model, PermissionMode: "full_approval"})
			if err != nil {
				t.Fatal(err)
			}
			response := f.request(t, http.MethodPost, "/v1/chat/sse", string(payload))
			data, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || calls.Load() != 2 {
				t.Fatal("retry must make exactly two provider requests", err)
			}
			for _, marker := range []string{key, prompt, bodySecret} {
				if strings.Contains(string(data), marker) {
					t.Fatal("retry exposed provider body through SSE")
				}
			}
			waitSecretChatOwners(t)
			turn, err := f.client.KaguyaChatTurn.Query().Only(f.ctx)
			wantStatus := kaguyachatturn.StatusFailed
			wantDone := 0
			if recovered {
				wantStatus = kaguyachatturn.StatusCompleted
				wantDone = 1
			}
			if err != nil || turn.Status != wantStatus || turn.UserContent != prompt {
				t.Fatal("retry lost its persistent turn outcome", err)
			}
			if strings.Count(string(data), `"flag":"done"`) != wantDone {
				t.Fatal("retry emitted an incorrect number of done frames")
			}
			if !recovered && !strings.Contains(string(data), fmt.Sprintf(`"code":%d`, dtocode.ChatSSEFailure.Code)) {
				t.Fatal("exhausted retry lost the fixed error mapping")
			}
			if f.logs.FilterMessageSnippet("retry chat stream:").Len() != 1 {
				t.Fatal("retry lost its single diagnostic warning")
			}
			requireSecretAbsentFromLogs(t, f.logs, key, prompt, bodySecret)
		})
	}
}

func TestApprovalHTTPStorageAndToolErrorsKeepSecretBoundaries(t *testing.T) {
	f := setupSecretChat(t)
	const key, pathSecret, storageSecret = "fake-read-provider-key", "fake-secret-missing-path", "fake-secret-decision-storage"
	home := t.TempDir()
	t.Setenv("HOME", home)
	project, err := f.client.KaguyaProject.Create().SetName("secret fixture").SetPath(home).Save(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	input, _ := json.Marshal(map[string]string{"path": pathSecret})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		delta, reason := map[string]any{"role": "assistant", "content": "done"}, "stop"
		if requests.Add(1) == 1 {
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "secret-read-call", "type": "function", "function": map[string]any{"name": "read", "arguments": string(input)}}}}
			reason = "tool_calls"
		} else {
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), pathSecret) {
				t.Error("tool's actionable error was lost before the follow-up model call")
			}
		}
		for _, choice := range []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}, {"index": 0, "delta": map[string]any{}, "finish_reason": reason}} {
			chunk, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "fixture", "choices": []any{choice}})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(remote.Close)
	var fail atomic.Bool
	f.client.KaguyaApproval.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			status, ok := mutation.(*ent.KaguyaApprovalMutation).Status()
			if ok && status == kaguyaapproval.StatusApproved && fail.Load() {
				return nil, errors.New(storageSecret)
			}
			return next.Mutate(ctx, mutation)
		})
	})
	model := f.model(t, remote.URL, key)
	payload, _ := json.Marshal(dtochat.ChatReq{Messages: "read missing file", ProjectID: project.ID, ModelID: model, PermissionMode: "full_approval"})
	response := f.request(t, http.MethodPost, "/v1/chat/sse", string(payload))
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var pending *dtochat.Approval
	var conversation string
	done, toolErrors := 0, 0
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame struct {
			Code int              `json:"code"`
			Data dtochat.ChatResp `json:"data"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil || frame.Code != dtocode.SystemSuccess.Code {
			t.Fatal("invalid secret-boundary SSE frame", err)
		}
		if strings.Contains(line, key) || strings.Contains(line, storageSecret) {
			t.Fatal("configuration/storage secret crossed SSE boundary")
		}
		if frame.Data.Chat.Flag == dtochat.ChatFlagDone {
			done++
		}
		if frame.Data.Chat.Block != nil && frame.Data.Chat.Block.Type == "tool_result" && frame.Data.Chat.Block.IsError && frame.Data.Chat.Block.Output != nil && strings.Contains(frame.Data.Chat.Block.Output.Text, pathSecret) {
			toolErrors++
		}
		if frame.Data.Approval == nil || frame.Data.Approval.Status != "pending" {
			continue
		}
		if pending != nil {
			t.Fatal("unexpected second pending approval")
		}
		pending, conversation = frame.Data.Approval, frame.Data.Chat.ID
		if pending.Input != string(input) || f.logs.FilterMessage("tool failed").Len() != 0 {
			t.Fatal("builtin input changed or tool ran before approval")
		}
		path := "/v1/chat/conversation/" + conversation + "/approvals/" + pending.ApprovalID
		fail.Store(true)
		failed := f.request(t, http.MethodPost, path, `{"decision":"approve"}`)
		body, readErr := io.ReadAll(failed.Body)
		failed.Body.Close()
		if readErr != nil || strings.Contains(string(body), storageSecret) || !strings.Contains(string(body), fmt.Sprintf(`"code":%d`, dtocode.ChatSSEFailure.Code)) {
			t.Fatal("storage failure was exposed or accepted", readErr)
		}
		row, err := f.client.KaguyaApproval.Query().Only(f.ctx)
		if err != nil || row.Status != kaguyaapproval.StatusPending || row.InputHash != permission.HashInput(string(input)) || f.logs.FilterMessage("tool failed").Len() != 0 {
			t.Fatal("failed approval changed authorization or executed the read", err)
		}
		fail.Store(false)
		approved := f.request(t, http.MethodPost, path, `{"decision":"approve"}`)
		var envelope struct {
			Code int `json:"code"`
		}
		err = json.NewDecoder(approved.Body).Decode(&envelope)
		approved.Body.Close()
		if err != nil || envelope.Code != dtocode.SystemSuccess.Code {
			t.Fatal("valid approval did not commit", err)
		}
	}
	if err := scanner.Err(); err != nil || pending == nil || done != 1 || toolErrors != 1 || requests.Load() != 2 {
		t.Fatal("approved tool error did not produce one completed turn", err, done, toolErrors)
	}
	waitSecretChatOwners(t)
	row, err := f.client.KaguyaApproval.Query().Only(f.ctx)
	if err != nil || row.Status != kaguyaapproval.StatusConsumed || row.ExecutionState != kaguyaapproval.ExecutionStateFailed || row.ExecutionReasonCode != "tool_failed" {
		t.Fatal("tool error audit misreported execution", err)
	}
	history := f.request(t, http.MethodGet, "/v1/chat/conversation/"+conversation+"/turns", "")
	body, err := io.ReadAll(history.Body)
	history.Body.Close()
	if err != nil || !strings.Contains(string(body), pathSecret) || strings.Contains(string(body), key) || strings.Contains(string(body), storageSecret) {
		t.Fatal("history lost tool result or exposed unrelated secrets", err)
	}
	if f.logs.FilterMessage("conversation API failed").Len() != 1 || f.logs.FilterMessage("tool failed").Len() != 1 {
		t.Fatal("storage/tool failures lost diagnostic events")
	}
	requireSecretAbsentFromLogs(t, f.logs, key, pathSecret, storageSecret)
}
