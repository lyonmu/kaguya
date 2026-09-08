package agent

import (
	"errors"
	"testing"
	"time"

	"charm.land/fantasy"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
)

func TestTurnTraceRejectsMissingToolResult(t *testing.T) {
	call := newChatStream(func(dtochat.ContentBlock) error { return nil }).callbacks()
	trace := newTurnTrace()
	trace.wrap(&call)
	if err := call.OnToolCall(fantasy.ToolCallContent{ToolCallID: "unfinished", ToolName: "search", Input: `{}`}); err != nil {
		t.Fatal(err)
	}
	if err := trace.finish(time.Now()); err == nil {
		t.Fatal("unfinished tool must not be persisted as complete")
	}
}

func TestTurnTracePreservesOrderAndMergesTools(t *testing.T) {
	stream := newChatStream(func(dtochat.ContentBlock) error { return nil })
	call := stream.callbacks()
	trace := newTurnTrace()
	trace.wrap(&call)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(call.OnStepStart(0))
	must(call.OnReasoningStart("0", fantasy.ReasoningContent{}))
	must(call.OnReasoningDelta("0", "think "))
	must(call.OnReasoningDelta("0", "first"))
	must(call.OnReasoningEnd("0", fantasy.ReasoningContent{Text: "think first"}))
	must(call.OnToolInputStart("a", "first-tool"))
	must(call.OnToolInputDelta("a", `{"a":`))
	must(call.OnToolCall(fantasy.ToolCallContent{ToolCallID: "a", ToolName: "first-tool", Input: `{"a":1}`}))
	must(call.OnToolInputStart("b", "second-tool"))
	must(call.OnToolCall(fantasy.ToolCallContent{ToolCallID: "b", ToolName: "second-tool", Input: `{}`}))
	must(call.OnToolResult(fantasy.ToolResultContent{ToolCallID: "b", ToolName: "second-tool", Result: fantasy.ToolResultOutputContentError{Error: errors.New("failed")}}))
	must(call.OnTextStart("0"))
	must(call.OnTextDelta("0", "waiting"))
	must(call.OnTextEnd("0"))
	must(call.OnToolResult(fantasy.ToolResultContent{ToolCallID: "a", ToolName: "first-tool", Result: fantasy.ToolResultOutputContentText{Text: "ok"}}))
	must(call.OnStepStart(1))
	must(call.OnTextStart("0")) // 下一次模型调用复用 ID 仍是新行，不与上一段正文合并
	must(call.OnTextDelta("0", "answer"))
	must(call.OnTextEnd("0"))
	must(trace.finish(time.Now()))
	b := trace.blocks
	if len(b) != 5 {
		t.Fatalf("blocks=%+v", b)
	}
	wantTypes := []dtochat.BlockType{dtochat.BlockTypeReasoning, dtochat.BlockTypeToolCall, dtochat.BlockTypeToolCall, dtochat.BlockTypeText, dtochat.BlockTypeText}
	for i, row := range b {
		if row.Type != wantTypes[i] || row.Sequence != int64(i+1) || row.StartOrder <= 0 || row.EndOrder < row.StartOrder || row.FinishedAt.Before(row.StartedAt) {
			t.Fatalf("row %d=%+v", i, row)
		}
	}
	if b[0].Text != "think first" || b[1].Input != `{"a":1}` || b[1].Output.Text != "ok" || !b[2].IsError || b[2].Output.Text != "failed" || b[3].Text != "waiting" || b[4].Text != "answer" {
		t.Fatalf("lost content: %+v", b)
	}
	if b[1].StartOrder >= b[2].StartOrder || b[1].EndOrder <= b[2].EndOrder {
		t.Fatal("parallel tool order lost")
	}
	if b[1].EndOrder <= b[3].EndOrder {
		t.Fatal("tool completion should be after intermediate text")
	}
	// 真正落库并读回验证合并行和先后序号，而不只是测试内存列表。
	ctx, _ := setupChatTest(t)
	turn := testCompletedTurn("123", 0)
	turn.Blocks = b
	if err := saveCompletedTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	page, err := (&AgentSvc{}).ConversationTurns(ctx, "123", &dtochat.TurnPageReq{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	got := page.Items[0].Blocks
	if len(got) != 5 || got[1].ToolCallID != "a" || got[2].ToolCallID != "b" || got[1].EndOrder <= got[2].EndOrder {
		t.Fatalf("stored order: %+v", got)
	}
}
