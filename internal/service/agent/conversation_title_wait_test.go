package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyatitlejob"
)

func TestConversationTitleWaitWithoutTask(t *testing.T) {
	ctx, _ := setupChatTest(t)
	if err := saveCompletedTurn(ctx, testCompletedTurn("123", 0)); err != nil {
		t.Fatal(err)
	}
	// 已失败、已结束或重启后没有任务，不根据“新对话”标题重新生成或轮询。
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	resp, err := (&AgentSvc{}).ConversationTitleWait(waitCtx, "123")
	if err != nil || resp.ID != "123" || resp.Title != defaultConversationTitle {
		t.Fatalf("no-task response: %+v, %v", resp, err)
	}
	if _, err := (&AgentSvc{}).ConversationTitleWait(ctx, "missing"); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("missing conversation: %v", err)
	}
}

// 首轮仍在生成且没有进行中的标题任务时，生成接口保留默认标题而不报错。
func TestConversationTitleGenerateWithoutSavedTurn(t *testing.T) {
	ctx, _ := setupChatTest(t)
	target := &chatTarget{model: &ent.KaguyaModelsInfo{ModelID: "test", ModelName: "test"}}
	if err := createConversation(ctx, target, "123", "", "首轮提问", time.Now()); err != nil {
		t.Fatal(err)
	}
	resp, err := (&AgentSvc{}).ConversationTitleGenerate(ctx, "123")
	if err != nil || resp.ID != "123" || resp.Title != defaultConversationTitle {
		t.Fatalf("in-flight first turn: %+v, %v", resp, err)
	}
}

func TestConversationTitleWaitRequestDeadline(t *testing.T) {
	ctx, client := setupChatTest(t)
	if err := saveCompletedTurn(ctx, testCompletedTurn("123", 0)); err != nil {
		t.Fatal(err)
	}
	// 手动制造一个永不完成的在途任务，验证请求 deadline 只结束本次等待。
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("wait").SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("task").SetModelID("wait-model").SetRequestPath("/v1/chat/completions").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetTaskModelID(model.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueConversationTitle(ctx, "123"); err != nil {
		t.Fatal(err)
	}
	// 等 worker 领取任务，确保等待发生在 running 状态。
	deadline := time.Now().Add(2 * time.Second)
	for {
		if status, statusErr := DefaultTitleQueue.jobStatus(ctx, "123"); statusErr == nil && status == kaguyatitlejob.StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("title job was not claimed")
		}
		time.Sleep(2 * time.Millisecond)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := (&AgentSvc{}).ConversationTitleWait(waitCtx, "123"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request deadline: %v", err)
	}
	// 请求 deadline 不能取消后台任务：任务仍处于在途状态，随后显式取消。
	if status, err := DefaultTitleQueue.jobStatus(ctx, "123"); err != nil || status != kaguyatitlejob.StatusRunning {
		t.Fatalf("background task was ended by request deadline: %v %v", status, err)
	}
	DefaultTitleQueue.Cancel("123")
	// 取消最终终结任务：请求 deadline 只影响本次等待，任务的取消来自显式 Cancel。
	deadline = time.Now().Add(2 * time.Second)
	for {
		if status, statusErr := DefaultTitleQueue.jobStatus(ctx, "123"); statusErr == nil &&
			status != kaguyatitlejob.StatusPending && status != kaguyatitlejob.StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled title job did not finish")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
