package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lyonmu/kaguya/internal/ent"
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
	if err := createConversation(ctx, target, "123", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	resp, err := (&AgentSvc{}).ConversationTitleGenerate(ctx, "123")
	if err != nil || resp.ID != "123" || resp.Title != defaultConversationTitle {
		t.Fatalf("in-flight first turn: %+v, %v", resp, err)
	}
}

func TestConversationTitleWaitRequestDeadline(t *testing.T) {
	ctx, _ := setupChatTest(t)
	if err := saveCompletedTurn(ctx, testCompletedTurn("123", 0)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	conversationTitles.Lock()
	conversationTitles.pending["123"] = done
	conversationTitles.Unlock()
	defer func() {
		conversationTitles.Lock()
		delete(conversationTitles.pending, "123")
		close(done)
		conversationTitles.Unlock()
	}()
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := (&AgentSvc{}).ConversationTitleWait(waitCtx, "123"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request deadline: %v", err)
	}
	select {
	case <-done:
		t.Fatal("request deadline closed background task")
	default:
	}
}
