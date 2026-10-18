package agent

import (
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

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/agent/permission"
	agentruntime "github.com/lyonmu/kaguya/internal/agent/runtime"
	"github.com/lyonmu/kaguya/internal/consts"

	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtoproject "github.com/lyonmu/kaguya/internal/dto/project"
	projectsvc "github.com/lyonmu/kaguya/internal/service/project"
)

// approvalChatServer 提供一个两步的假模型：先请求 write，再报告工具结果。
func approvalChatServer(t *testing.T, results chan<- string) *httptest.Server {
	return approvalBatchChatServer(t, results, 1)
}

func approvalBatchChatServer(t *testing.T, results chan<- string, count int) *httptest.Server {
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
			calls := make([]any, count)
			for i := range calls {
				calls[i] = map[string]any{"index": i, "id": fmt.Sprintf("call-%d", i+1), "type": "function", "function": map[string]any{"name": "write", "arguments": `{"path":"approved.txt","content":"ok"}`}}
			}
			emit(map[string]any{"role": "assistant", "tool_calls": calls}, nil)
			emit(map[string]any{}, "tool_calls")
		} else {
			results <- string(body.Messages)
			emit(map[string]any{"role": "assistant", "content": "done"}, nil)
			emit(map[string]any{}, "stop")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// 走真实 Fantasy 并行调度：前五个等待审批，第六个必须在一个槽位释放后才登记。
// 拒绝、重复批准与乱序裁决均不能让同一调用执行两次，所有发帧保持轮内 seq 顺序。
// 资源fixture在真实前五个已登记且第六个仍排队时取端点；普通行为测试为空。
var approvalBatchResourceProbe func(*testing.T, int)

func TestParallelApprovalBatchDoesNotReplayOrLoseQueuedCall(t *testing.T) {
	ctx, client := setupChatTest(t)
	seedApprovalTurn(t, ctx, client)
	frames := make(chan *dtochat.ChatResp)
	broker := newApprovalBroker("conv-1", "turn-1", newTurnEmitter(ctx, frames))
	defer broker.Close()
	results := make(chan string, 1)
	server := approvalBatchChatServer(t, results, 6)
	defer server.Close()
	var runs [6]atomic.Int64
	tool := fantasy.NewParallelAgentTool("write", "fixture", func(_ context.Context, _ struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
		var index int
		if _, err := fmt.Sscanf(call.ID, "call-%d", &index); err != nil || index < 1 || index > len(runs) {
			return fantasy.ToolResponse{}, fmt.Errorf("unexpected fixture call ID")
		}
		runs[index-1].Add(1)
		return fantasy.NewTextResponse("executed " + call.ID), nil
	})
	wrapped := permission.WrapAll(permission.ModeFullApproval, broker, []permission.GatedTool{{Tool: tool, Class: permission.Classification{Identity: "builtin:write", Effect: permission.EffectMutating}}})
	if !wrapped[0].Info().Parallel {
		t.Fatal("permission wrapper lost parallel metadata")
	}
	agent, err := agentruntime.New(agentruntime.WithProvider(agentruntime.ProviderConfig{
		Protocol: consts.ProtocolOpenAIChat, BaseURL: server.URL, RequestPath: "/v1/chat/completions", APIKey: "fixture", ModelID: "test",
	}), agentruntime.WithTools(wrapped...))
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := agent.Stream(ctx, fantasy.AgentStreamCall{Prompt: "fixture"})
		broker.Close()
		close(frames)
		finished <- err
	}()
	var initial []dtochat.Approval
	var seq int64
	for len(initial) < 5 {
		select {
		case frame, ok := <-frames:
			if !ok || frame.Approval == nil || frame.Approval.Status != "pending" || frame.Seq <= seq {
				t.Fatal("parallel approvals did not register five ordered pending frames")
			}
			seq = frame.Seq
			initial = append(initial, *frame.Approval)
		case <-ctx.Done():
			t.Fatal("five approvals did not become ready")
		}
	}
	broker.mu.Lock()
	pending := len(broker.pending)
	broker.mu.Unlock()
	if pending != 5 {
		t.Fatalf("sixth call bypassed Fantasy parallel limit: pending=%d", pending)
	}
	if approvalBatchResourceProbe != nil {
		approvalBatchResourceProbe(t, pending)
	}
	for i := range runs {
		if runs[i].Load() != 0 {
			t.Fatal("tool executed before any approval")
		}
	}
	decisions := make(chan error, 1)
	rejected := map[string]bool{initial[1].ToolCallID: true, initial[3].ToolCallID: true}
	go func() {
		for i := len(initial) - 1; i >= 0; i-- {
			decision := "approve"
			if rejected[initial[i].ToolCallID] {
				decision = "reject"
			}
			for repeat := 0; repeat < 2; repeat++ {
				if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", initial[i].ApprovalID, decision); err != nil {
					decisions <- err
					return
				}
			}
		}
		decisions <- nil
	}()
	seen := 5
	for frame := range frames {
		if frame.Approval == nil || frame.Seq <= seq {
			t.Fatal("approval emitter lost ordering")
		}
		seq = frame.Seq
		if frame.Approval.Status == "pending" {
			seen++
			if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", frame.Approval.ApprovalID, "approve"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := <-decisions; err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if seen != 6 {
		t.Fatalf("queued sixth approval lost: seen=%d", seen)
	}
	for i := range runs {
		want := int64(1)
		if rejected[fmt.Sprintf("call-%d", i+1)] {
			want = 0
		}
		if runs[i].Load() != want {
			t.Fatalf("call-%d runs=%d want=%d", i+1, runs[i].Load(), want)
		}
	}
	approvalRegistry.Lock()
	remaining := len(approvalRegistry.entries)
	approvalRegistry.Unlock()
	if remaining != 0 {
		t.Fatal("completed batch retained registered approvals")
	}
	select {
	case messages := <-results:
		if strings.Count(messages, "executed call-") != 4 || strings.Count(messages, "rejected by the user") != 2 {
			t.Fatal("model did not receive each approved/rejected tool result once")
		}
	case <-time.After(time.Second):
		t.Fatal("model did not receive batch results")
	}
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
