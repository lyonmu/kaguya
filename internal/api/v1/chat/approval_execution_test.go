package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaapproval"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
	"github.com/lyonmu/kaguya/internal/global"
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// 真实 HTTP API → service → broker/CAS → wrapper → write 工厂；
// fake模型仅代替外部推理服务，不替代权限实现或主机副作用。
func TestApprovalHTTPExecutionAndAudit(t *testing.T) {
	for _, decision := range []string{"approve", "reject", "disconnect"} {
		t.Run(decision, func(t *testing.T) {
			client, engine := setupApprovalAPI(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			ctx := context.Background()
			project, err := client.KaguyaProject.Create().SetName("HTTP approval").SetPath(home).Save(ctx)
			if err != nil {
				t.Fatal(err)
			}
			const marker = "fake-secret-approval-input"
			core, logs := observer.New(zap.DebugLevel)
			previousLogger := global.Logger
			global.Logger = zap.New(core)
			t.Cleanup(func() { global.Logger = previousLogger })
			var requests atomic.Int64
			modelHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				var delta any
				reason := "stop"
				if requests.Add(1) == 1 {
					input, _ := json.Marshal(map[string]string{"path": "approved.txt", "content": marker})
					delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "http-call", "type": "function", "function": map[string]any{"name": "write", "arguments": string(input)}}}}
					reason = "tool_calls"
				} else {
					delta = map[string]any{"role": "assistant", "content": "done"}
				}
				chunk := func(delta any, reason any) {
					encoded, _ := json.Marshal(map[string]any{"id": "test", "object": "chat.completion.chunk", "model": "test", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}})
					fmt.Fprintf(w, "data: %s\n\n", encoded)
				}
				chunk(delta, nil)
				chunk(map[string]any{}, reason)
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			t.Cleanup(modelHost.Close)
			provider, err := client.KaguyaProviderInfo.Create().SetProviderName("fixture").SetAPIKey("fake-secret-http-provider").SetBaseURL(modelHost.URL).Save(ctx)
			if err != nil {
				t.Fatal(err)
			}
			model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").SetRequestPath("/v1/chat/completions").Save(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ended := make(chan struct{})
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/chat/sse" {
					defer close(ended)
				}
				engine.ServeHTTP(w, r)
			}))
			transport := &http.Transport{}
			httpClient := &http.Client{Transport: transport, Timeout: 10 * time.Second}
			t.Cleanup(func() {
				transport.CloseIdleConnections()
				host.CloseClientConnections()
				host.Close()
				deadline := time.Now().Add(5 * time.Second)
				for serviceagent.PendingWork() != 0 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if serviceagent.PendingWork() != 0 {
					t.Fatal("chat owner did not join before globals restored")
				}
			})
			payload, _ := json.Marshal(dtochat.ChatReq{Messages: "create file", ProjectID: project.ID, ModelID: model.ID, PermissionMode: "full_approval"})
			response, err := httpClient.Post(host.URL+"/v1/chat/sse", "application/json", strings.NewReader(string(payload)))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			scanner := bufio.NewScanner(response.Body)
			scanner.Buffer(make([]byte, 4096), 1<<20)
			var pending *dtochat.Approval
			var conversationID string
			doneFrames := 0
			var seq int64
			for scanner.Scan() {
				line := scanner.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var frame struct {
					Code int              `json:"code"`
					Data dtochat.ChatResp `json:"data"`
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
					t.Fatal(err)
				}
				if frame.Code != 100000 {
					t.Fatalf("unexpected SSE code: %d", frame.Code)
				}
				if frame.Data.Seq <= seq {
					t.Fatal("SSE sequence regressed")
				}
				seq = frame.Data.Seq
				if frame.Data.Chat.Flag == dtochat.ChatFlagDone {
					doneFrames++
				}
				if frame.Data.Approval == nil || frame.Data.Approval.Status != "pending" {
					continue
				}
				if pending != nil {
					t.Fatal("model replayed the write approval")
				}
				pending, conversationID = frame.Data.Approval, frame.Data.Chat.ID
				if _, err := os.Stat(filepath.Join(home, "approved.txt")); !os.IsNotExist(err) {
					t.Fatal("write happened before approval")
				}
				if decision == "disconnect" {
					response.Body.Close()
					break
				}
				path := "/v1/chat/conversation/" + conversationID + "/approvals/" + pending.ApprovalID
				// 会话绑定不能被请求者换掉，也不泄露其它会话的授权入口。
				wrong, err := httpClient.Post(host.URL+"/v1/chat/conversation/wrong/approvals/"+pending.ApprovalID, "application/json", strings.NewReader(`{"decision":"approve"}`))
				if err != nil {
					t.Fatal(err)
				}
				var failure struct {
					Code int `json:"code"`
				}
				decodeErr := json.NewDecoder(wrong.Body).Decode(&failure)
				wrong.Body.Close()
				if decodeErr != nil || failure.Code != 102012 {
					t.Fatal("cross-conversation approval was accepted")
				}
				for repeat := 0; repeat < 2; repeat++ {
					resolved, err := httpClient.Post(host.URL+path, "application/json", strings.NewReader(`{"decision":"`+decision+`"}`))
					if err != nil {
						t.Fatal(err)
					}
					var envelope struct {
						Code int `json:"code"`
					}
					decodeErr := json.NewDecoder(resolved.Body).Decode(&envelope)
					resolved.Body.Close()
					if decodeErr != nil || envelope.Code != 100000 {
						t.Fatalf("decision repeat %d failed: %v code=%d", repeat, decodeErr, envelope.Code)
					}
				}
			}
			if pending == nil {
				t.Fatal("no real pending approval received")
			}
			if decision != "disconnect" && scanner.Err() != nil {
				t.Fatal(scanner.Err())
			}
			select {
			case <-ended:
			case <-time.After(3 * time.Second):
				t.Fatal("SSE handler retained after decision/disconnect")
			}
			deadline := time.Now().Add(5 * time.Second)
			for serviceagent.PendingWork() != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if serviceagent.PendingWork() != 0 {
				t.Fatal("chat producer retained after SSE exit")
			}
			row, err := client.KaguyaApproval.Query().Where(kaguyaapproval.ApprovalIDEQ(pending.ApprovalID)).Only(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus, wantExecution := kaguyaapproval.StatusConsumed, kaguyaapproval.ExecutionStateSucceeded
			if decision == "reject" {
				wantStatus, wantExecution = kaguyaapproval.StatusRejected, kaguyaapproval.ExecutionStateNotStarted
			}
			if decision == "disconnect" {
				wantStatus, wantExecution = kaguyaapproval.StatusCanceled, kaguyaapproval.ExecutionStateNotStarted
			}
			if row.Status != wantStatus || row.ExecutionState != wantExecution || row.ConversationID != conversationID || row.ToolCallID != "http-call" || row.ToolIdentity != "builtin:write" {
				t.Fatalf("audit did not match execution: status=%s execution=%s", row.Status, row.ExecutionState)
			}
			content, fileErr := os.ReadFile(filepath.Join(home, "approved.txt"))
			if decision == "approve" {
				if fileErr != nil || string(content) != marker || row.ConsumedAt == nil || row.ExecutionFinishedAt == nil {
					t.Fatal("approved write did not complete exactly once")
				}
			} else if !os.IsNotExist(fileErr) || row.ConsumedAt != nil {
				t.Fatal("undecided/rejected write left a side effect")
			}
			turn, err := client.KaguyaChatTurn.Query().Where(kaguyachatturn.ConversationIDEQ(conversationID)).Only(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wantTurn := kaguyachatturn.StatusCompleted
			if decision == "disconnect" {
				wantTurn = kaguyachatturn.StatusInterrupted
			}
			if turn.Status != wantTurn || (decision != "disconnect" && doneFrames != 1) {
				t.Fatalf("turn=%s done=%d", turn.Status, doneFrames)
			}
			encodedAudit, _ := json.Marshal(row)
			if strings.Contains(string(encodedAudit), marker) {
				t.Fatal("approval audit persisted plaintext input")
			}
			for _, entry := range logs.All() {
				encoded, _ := json.Marshal(entry.ContextMap())
				if strings.Contains(entry.Message+string(encoded), marker) || strings.Contains(entry.Message+string(encoded), "fake-secret-http-provider") {
					t.Fatal("execution leaked input/provider secret to logs")
				}
			}
		})
	}
}
