package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	token "github.com/lyonmu/kaguya/internal/agent/token"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatblock"
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

func TestCompactHistoryDefersLargeBlocksAndPreservesPaging(t *testing.T) {
	ctx, _ := setupChatTest(t)
	svc := &AgentSvc{}
	large := strings.Repeat("large-payload-", 40000)
	for i := int64(0); i < 7; i++ {
		turn := testCompletedTurn("compact-history", i)
		turn.Blocks[0].Text = large
		turn.Blocks[1].Input = large
		turn.Blocks[1].Output.Text = large
		turn.ContextMessages = []fantasy.Message{fantasy.NewUserMessage(large)}
		if err := saveCompletedTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.ConversationTurns(ctx, "compact-history", &dtochat.TurnPageReq{Limit: 5, Page: 1, Compact: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 5 || page.TotalPages != 2 || page.Items[4].TurnIndex != 5 {
		t.Fatalf("page=%+v", page)
	}
	data, err := json.Marshal(page)
	if err != nil || len(data) > 12000 || strings.Contains(string(data), "large-payload-") {
		t.Fatalf("folded payload leaked: bytes=%d err=%v", len(data), err)
	}
	for _, turn := range page.Items {
		if !turn.Blocks[0].DetailsDeferred || !turn.Blocks[1].DetailsDeferred || !turn.Blocks[1].HasOutput || turn.Blocks[2].Text != "回答" || turn.Blocks[2].DetailsDeferred {
			t.Fatalf("preview lost state: %+v", turn.Blocks)
		}
	}
	last, err := svc.ConversationTurns(ctx, "compact-history", &dtochat.TurnPageReq{Limit: 5, Page: 1000000, Compact: true})
	if err != nil || last.Page != 2 || len(last.Items) != 2 || last.Items[0].TurnIndex != 6 {
		t.Fatalf("last=%+v %v", last, err)
	}
	older, err := svc.ConversationTurns(ctx, "compact-history", &dtochat.TurnPageReq{Limit: 5, Before: last.NextBefore, Compact: true})
	if err != nil || older.HasMore || len(older.Items) != 5 || older.Items[0].TurnIndex != 1 {
		t.Fatalf("older=%+v %v", older, err)
	}
	request := &dtochat.BlockDetailReq{ID: "compact-history", TurnIndex: 2, Sequence: 2}
	block, err := svc.ConversationBlock(ctx, request)
	if err != nil || block.Input != large || block.Output == nil || block.Output.Text != large || block.DetailsDeferred {
		t.Fatalf("full tool block unavailable: %v", err)
	}
	request.ID = "another-conversation"
	if _, err := svc.ConversationBlock(ctx, request); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("cross-conversation lookup: %v", err)
	}
	request.ID = "compact-history"
	if err := svc.ConversationDelete(ctx, request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConversationBlock(ctx, request); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("deleted history exposed: %v", err)
	}
}

// 最后一次压缩快照之前的原始消息不再参与续聊拼接。
func TestLoadConversationUsesLatestSnapshot(t *testing.T) {
	ctx, _ := setupChatTest(t)
	if err := saveCompletedTurn(ctx, testCompletedTurn("snapshot", 0)); err != nil {
		t.Fatal(err)
	}
	second := testCompletedTurn("snapshot", 1)
	second.ContextMessages = []fantasy.Message{fantasy.NewUserMessage("第一次快照")}
	second.CompactionCount = 1
	if err := saveCompletedTurn(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := saveCompletedTurn(ctx, testCompletedTurn("snapshot", 2)); err != nil {
		t.Fatal(err)
	}
	fourth := testCompletedTurn("snapshot", 3)
	fourth.ContextMessages = []fantasy.Message{fantasy.NewUserMessage("第二次快照")}
	fourth.CompactionCount = 1
	if err := saveCompletedTurn(ctx, fourth); err != nil {
		t.Fatal(err)
	}
	fifth := testCompletedTurn("snapshot", 4)
	if err := saveCompletedTurn(ctx, fifth); err != nil {
		t.Fatal(err)
	}
	history, version, err := loadConversation(ctx, "snapshot")
	if err != nil || version != 5 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	want := append([]fantasy.Message{fantasy.NewUserMessage("第二次快照")}, fifth.Messages...)
	gotJSON, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("history=%s want=%s", gotJSON, wantJSON)
	}
	if strings.Contains(string(gotJSON), "第一次快照") {
		t.Fatal("earlier snapshot leaked into continuation")
	}
}

// block 超过单批上限时仍按顺序完整落库。
func TestSaveCompletedTurnPersistsManyBlocks(t *testing.T) {
	ctx, client := setupChatTest(t)
	turn := testCompletedTurn("many-blocks", 0)
	turn.Blocks = make([]dtochat.StoredBlock, 501)
	for i := range turn.Blocks {
		turn.Blocks[i] = dtochat.StoredBlock{Sequence: int64(i + 1), Type: dtochat.BlockTypeText, Text: fmt.Sprintf("block-%d", i+1),
			StartedAt: turn.StartedAt, FinishedAt: turn.FinishedAt, StartOrder: int64(i + 1), EndOrder: int64(i + 2)}
	}
	if err := saveCompletedTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	rows, err := client.KaguyaChatBlock.Query().Order(kaguyachatblock.BySequence()).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(turn.Blocks) {
		t.Fatalf("blocks=%d want %d", len(rows), len(turn.Blocks))
	}
	for i, row := range rows {
		if row.Sequence != int64(i+1) || row.Text != fmt.Sprintf("block-%d", i+1) {
			t.Fatalf("block %d: %+v", i, row)
		}
	}
}
