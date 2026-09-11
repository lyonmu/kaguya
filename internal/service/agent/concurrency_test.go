package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	agentruntime "github.com/lyonmu/kaguya/internal/agent/runtime"
	"github.com/lyonmu/kaguya/internal/consts"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/global"
)

// 占满槽位后 Chat 必须快速失败，不能继续查询历史或调用提供商。
func TestChatRejectsWhenConcurrencyLimited(t *testing.T) {
	ctx, client := setupChatTest(t)
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("limit").SetAPIProtocol(consts.ProtocolOpenAIChat).SetAPIKey("test").SetBaseURL("http://127.0.0.1:1").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("limit").SetModelID("limit").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxConcurrentChats; i++ {
		if !tryAcquire(chatSlots) {
			t.Fatalf("chat slot %d unavailable", i)
		}
	}
	defer func() {
		for i := 0; i < maxConcurrentChats; i++ {
			releaseSlot(chatSlots)
		}
	}()
	frames := make(chan *dtochat.ChatResp, 4)
	(&AgentSvc{}).Chat(ctx, frames, &dtochat.ChatReq{ID: "limited", ModelID: model.ID, Messages: "hi"})
	var got error
	for frame := range frames {
		if frame.Err != nil {
			got = frame.Err
		}
	}
	if !errors.Is(got, ErrChatConcurrencyLimited) {
		t.Fatalf("err=%v", got)
	}
}

// 标题任务满员时直接跳过，不注册任务也不访问数据库。
func TestStartConversationTitleSkipsWhenBusy(t *testing.T) {
	_, client := setupChatTest(t)
	for i := 0; i < maxConcurrentTitleTasks; i++ {
		if !tryAcquire(titleSlots) {
			t.Fatalf("title slot %d unavailable", i)
		}
	}
	defer func() {
		for i := 0; i < maxConcurrentTitleTasks; i++ {
			releaseSlot(titleSlots)
		}
	}()
	result := startConversationTitle(client, global.Logger, agentruntime.ProviderConfig{ConversationID: "busy-title"}, "q", "a")
	if _, ok := <-result; ok {
		t.Fatal("title task started while slots are full")
	}
}

func TestWaitActive(t *testing.T) {
	done := beginWork()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancelWait()
	if err := WaitActive(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	done()
	if err := WaitActive(context.Background()); err != nil {
		t.Fatalf("err=%v", err)
	}
}
