package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	token "github.com/lyonmu/kaguya/internal/agent/token"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
)

func testCompletedTurn(id string, version int64) completedTurn {
	at := time.Now()
	return completedTurn{ConversationID: id, Version: version, UserContent: "你好", ProviderName: "test", ModelName: "test", ModelID: "test", APIProtocol: "openai-chat",
		StartedAt: at, FinishedAt: at.Add(time.Second), FinishReason: "stop",
		Usage:    token.NormalizedUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 35, CacheHitTokens: 5, ReasoningTokens: 7},
		Messages: []fantasy.Message{fantasy.NewUserMessage("你好"), {Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "回答"}}}},
		Blocks: []dtochat.StoredBlock{{Sequence: 1, Type: dtochat.BlockTypeReasoning, Text: "思考", StartedAt: at, FinishedAt: at.Add(time.Millisecond), StartOrder: 1, EndOrder: 2},
			{Sequence: 2, Type: dtochat.BlockTypeToolCall, ToolCallID: "tool", ToolName: "search", Input: `{"q":"go"}`, Output: &dtochat.ToolOutput{Type: dtochat.ToolOutputText, Text: "找到"}, StartedAt: at, FinishedAt: at.Add(2 * time.Millisecond), StartOrder: 3, EndOrder: 4},
			{Sequence: 3, Type: dtochat.BlockTypeText, Text: "回答", StartedAt: at, FinishedAt: at.Add(3 * time.Millisecond), StartOrder: 5, EndOrder: 6}}}
}

func TestConversationPersistenceAndPages(t *testing.T) {
	ctx, _ := setupChatTest(t)
	if history, version, err := loadConversation(ctx, "unknown"); err != nil || version != 0 || history != nil {
		t.Fatalf("unknown: %+v %d %v", history, version, err)
	}
	svc := &AgentSvc{}
	for i := int64(0); i < 3; i++ {
		if err := saveCompletedTurn(ctx, testCompletedTurn("123", i)); err != nil {
			t.Fatal(err)
		}
	}
	history, version, err := loadConversation(ctx, "123")
	if err != nil || version != 3 || len(history) != 6 {
		t.Fatalf("history=%+v version=%d err=%v", history, version, err)
	}
	history[0].Role = fantasy.MessageRoleAssistant
	again, _, err := loadConversation(ctx, "123")
	if err != nil || again[0].Role != fantasy.MessageRoleUser {
		t.Fatal("external mutation changed persisted history")
	}
	detail, err := svc.ConversationDetail(ctx, "123")
	if err != nil {
		t.Fatal(err)
	}
	if detail.TurnCount != 3 || detail.Usage.TotalTokens != 105 || detail.Usage.CachedTokens != 15 || detail.ToolCalls != 3 {
		t.Fatalf("bad totals: %+v", detail)
	}
	page, err := svc.ConversationTurns(ctx, "123", &dtochat.TurnPageReq{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || page.NextBefore != 2 || len(page.Items) != 2 || page.Items[0].TurnIndex != 2 || page.Items[1].TurnIndex != 3 {
		t.Fatalf("bad page: %+v", page)
	}
	blocks := page.Items[0].Blocks
	if len(blocks) != 3 || blocks[0].Type != dtochat.BlockTypeReasoning || blocks[1].Input != `{"q":"go"}` || blocks[1].Output.Text != "找到" || blocks[2].Type != dtochat.BlockTypeText {
		t.Fatalf("bad order: %+v", blocks)
	}
	older, err := svc.ConversationTurns(ctx, "123", &dtochat.TurnPageReq{Limit: 2, Before: page.NextBefore})
	if err != nil || older.HasMore || len(older.Items) != 1 || older.Items[0].TurnIndex != 1 {
		t.Fatalf("older=%+v %v", older, err)
	}
	yes, no := true, false
	title := "新的标题"
	if _, err := svc.ConversationUpdate(ctx, "123", &dtochat.ConversationUpdateReq{Title: &title, Favorite: &yes}); err != nil {
		t.Fatal(err)
	}
	list, err := svc.ConversationPage(ctx, &dtochat.ConversationPageReq{Keyword: "新的", Favorite: &yes, Page: 1, PageSize: 20})
	if err != nil || list.Total != 1 || list.Items[0].Title != title {
		t.Fatalf("list=%+v %v", list, err)
	}
	if _, err := svc.ConversationUpdate(ctx, "123", &dtochat.ConversationUpdateReq{Favorite: &no}); err != nil {
		t.Fatal(err)
	}
	list, err = svc.ConversationPage(ctx, &dtochat.ConversationPageReq{Favorite: &yes, Page: 1, PageSize: 20})
	if err != nil || list.Total != 0 {
		t.Fatalf("favorite=false failed: %+v %v", list, err)
	}
	if err := svc.ConversationDelete(ctx, "123"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConversationTurns(ctx, "123", &dtochat.TurnPageReq{Limit: 20}); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("deleted history visible: %v", err)
	}
	if _, _, err := loadConversation(ctx, "123"); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("deleted conversation resumable: %v", err)
	}
	list, err = svc.ConversationPage(ctx, &dtochat.ConversationPageReq{Page: 1, PageSize: 20})
	if err != nil || list.Total != 0 {
		t.Fatal("deleted conversation listed")
	}
}

func TestConversationTransactionRollback(t *testing.T) {
	ctx, client := setupChatTest(t)
	original := testCompletedTurn("123", 0)
	if err := saveCompletedTurn(ctx, original); err != nil {
		t.Fatal(err)
	}
	bad := testCompletedTurn("123", 1)
	bad.Blocks[2].Sequence = 1 // 写到第三个块时触发唯一索引错误，必须回滚摘要、轮次、此前两个块。
	if err := saveCompletedTurn(ctx, bad); err == nil {
		t.Fatal("expected duplicate sequence error")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := saveCompletedTurn(canceled, testCompletedTurn("123", 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error: %v", err)
	}
	if err := saveCompletedTurn(ctx, testCompletedTurn("123", 2)); !errors.Is(err, ErrConversationBusy) {
		t.Fatalf("stale version: %v", err)
	}
	detail, err := (&AgentSvc{}).ConversationDetail(ctx, "123")
	if err != nil || detail.TurnCount != 1 || detail.Usage.TotalTokens != 35 {
		t.Fatalf("rollback summary: %+v %v", detail, err)
	}
	if n, err := client.KaguyaChatTurn.Query().Count(ctx); err != nil || n != 1 {
		t.Fatalf("turns=%d %v", n, err)
	}
	if n, err := client.KaguyaChatBlock.Query().Count(ctx); err != nil || n != 3 {
		t.Fatalf("blocks=%d %v", n, err)
	}
	bad.ConversationID, bad.Version = "new", 0
	if err := saveCompletedTurn(ctx, bad); err == nil {
		t.Fatal("expected failed new conversation")
	}
	if n, err := client.KaguyaConversation.Query().Count(ctx); err != nil || n != 1 {
		t.Fatalf("failed new conversation leaked: %d %v", n, err)
	}
}

func TestConversationConcurrentGuard(t *testing.T) {
	_, _ = setupChatTest(t)
	release, err := acquireConversation("123")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	done := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func() {
			unlock, err := acquireConversation("123")
			if unlock != nil {
				unlock()
			}
			done <- err
		}()
	}
	for i := 0; i < 20; i++ {
		if err := <-done; !errors.Is(err, ErrConversationBusy) {
			t.Fatalf("concurrent acquire: %v", err)
		}
	}
	unlock, err := acquireConversation("other")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestConversationContextRoundTrip(t *testing.T) {
	ctx, _ := setupChatTest(t)
	turn := testCompletedTurn("123", 0)
	// 包括推理元数据、工具参数与错误输出，不允许用展示块拼接替代模型上下文。
	turn.Messages = append(turn.Messages,
		fantasy.Message{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{
			fantasy.ReasoningPart{Text: "reasoning", ProviderOptions: fantasy.ProviderOptions{anthropic.Name: &anthropic.ReasoningOptionMetadata{Signature: "private-signature"}}},
			fantasy.ToolCallPart{ToolCallID: "tool", ToolName: "search", Input: `{"q":"go"}`},
		}},
		fantasy.Message{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{
			fantasy.ToolResultPart{ToolCallID: "tool", Output: fantasy.ToolResultOutputContentError{Error: errors.New("failed")}},
		}})
	if err := saveCompletedTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	history, _, err := loadConversation(ctx, "123")
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(turn.Messages)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("context changed:\nwant %s\ngot %s", want, got)
	}
}
