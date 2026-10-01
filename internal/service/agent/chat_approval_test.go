package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtoproject "github.com/lyonmu/kaguya/internal/dto/project"
	projectsvc "github.com/lyonmu/kaguya/internal/service/project"
)

// approvalChatServer 提供一个两步的假模型：先请求 write，再报告工具结果。
func approvalChatServer(t *testing.T, results chan<- string) *httptest.Server {
	t.Helper()
	var requests atomic.Int64
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(delta any, reason any) {
			chunk := map[string]any{"id": "test", "object": "chat.completion.chunk", "model": "test", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}}
			b, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		if requests.Add(1) == 1 {
			emit(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call-1", "type": "function", "function": map[string]any{"name": "write", "arguments": `{"path":"approved.txt","content":"ok"}`}}}}, nil)
			emit(map[string]any{}, "tool_calls")
		} else {
			results <- string(body.Messages)
			emit(map[string]any{"role": "assistant", "content": "done"}, nil)
			emit(map[string]any{}, "stop")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func TestChatFullApprovalBlocksUntilApproved(t *testing.T) {
	ctx, client := setupChatTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, err := (&projectsvc.ProjectSvc{}).Save(ctx, "", &dtoproject.SaveReq{Name: "approval", Path: home})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan string, 1)
	server := approvalChatServer(t, results)
	defer server.Close()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("test").SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").SetRequestPath("/v1/chat/completions").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}

	frames := make(chan *dtochat.ChatResp)
	go (&AgentSvc{}).Chat(ctx, frames, &dtochat.ChatReq{
		ProjectID: p.ID, ModelID: model.ID, Messages: "create file",
		PermissionMode: "full_approval",
	})
	done, approvals := 0, 0
	approved := make(chan struct{})
	fileExistedAtApproval := false
	for frame := range frames {
		if frame.Err != nil {
			t.Fatal(frame.Err)
		}
		if frame.Approval != nil {
			approvals++
			if frame.Approval.Status == "pending" {
				if _, err := os.Stat(filepath.Join(home, "approved.txt")); err == nil {
					fileExistedAtApproval = true
				}
				go func(id string) {
					defer close(approved)
					if _, err := (&AgentSvc{}).DecideApproval(ctx, frame.Chat.ID, id, "approve"); err != nil {
						t.Errorf("approve failed: %v", err)
					}
				}(frame.Approval.ApprovalID)
			}
		}
		if frame.Chat.Flag == dtochat.ChatFlagDone {
			done++
		}
	}
	if done != 1 || approvals < 2 {
		t.Fatalf("done=%d approvals=%d", done, approvals)
	}
	if fileExistedAtApproval {
		t.Fatal("file must not exist before approval")
	}
	b, err := os.ReadFile(filepath.Join(home, "approved.txt"))
	if err != nil || string(b) != "ok" {
		t.Fatalf("approved file = %q, err=%v", b, err)
	}
	if messages := <-results; !strings.Contains(messages, "Successfully wrote") {
		t.Fatalf("model did not see tool result: %s", messages)
	}
}

func TestChatFullApprovalRejectHasNoSideEffect(t *testing.T) {
	ctx, client := setupChatTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, err := (&projectsvc.ProjectSvc{}).Save(ctx, "", &dtoproject.SaveReq{Name: "approval", Path: home})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan string, 1)
	server := approvalChatServer(t, results)
	defer server.Close()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("test").SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").SetRequestPath("/v1/chat/completions").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	frames := make(chan *dtochat.ChatResp)
	go (&AgentSvc{}).Chat(ctx, frames, &dtochat.ChatReq{
		ProjectID: p.ID, ModelID: model.ID, Messages: "create file",
		PermissionMode: "full_approval",
	})
	done := 0
	for frame := range frames {
		if frame.Err != nil {
			t.Fatal(frame.Err)
		}
		if frame.Approval != nil && frame.Approval.Status == "pending" {
			if _, err := (&AgentSvc{}).DecideApproval(ctx, frame.Chat.ID, frame.Approval.ApprovalID, "reject"); err != nil {
				t.Fatal(err)
			}
		}
		if frame.Chat.Flag == dtochat.ChatFlagDone {
			done++
		}
	}
	if done != 1 {
		t.Fatalf("done=%d", done)
	}
	if _, err := os.Stat(filepath.Join(home, "approved.txt")); !os.IsNotExist(err) {
		t.Fatalf("rejected call must not create the file: %v", err)
	}
	if messages := <-results; !strings.Contains(messages, "rejected") {
		t.Fatalf("model did not see rejection result: %s", messages)
	}
}

// 审批等待不应被断联以外的取消误伤：会话 busy 期间 PUT 修改模式被拒绝。
func TestConversationUpdateBusyDuringApproval(t *testing.T) {
	ctx, client := setupChatTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, err := (&projectsvc.ProjectSvc{}).Save(ctx, "", &dtoproject.SaveReq{Name: "approval", Path: home})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan string, 1)
	server := approvalChatServer(t, results)
	defer server.Close()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("test").SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").SetRequestPath("/v1/chat/completions").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	frames := make(chan *dtochat.ChatResp)
	go (&AgentSvc{}).Chat(ctx, frames, &dtochat.ChatReq{
		ProjectID: p.ID, ModelID: model.ID, Messages: "create file",
		PermissionMode: "full_approval",
	})
	busySeen := make(chan error, 1)
	for frame := range frames {
		if frame.Err != nil {
			t.Fatal(frame.Err)
		}
		if frame.Approval != nil && frame.Approval.Status == "pending" {
			title := "renamed"
			_, err := (&AgentSvc{}).ConversationUpdate(ctx, frame.Chat.ID, &dtochat.ConversationUpdateReq{Title: &title, PermissionMode: strPtr("full_access")})
			busySeen <- err
			if _, err := (&AgentSvc{}).DecideApproval(ctx, frame.Chat.ID, frame.Approval.ApprovalID, "approve"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := <-busySeen; err != ErrConversationBusy {
		t.Fatalf("update during approval = %v, want busy", err)
	}
}

func strPtr(value string) *string { return &value }
