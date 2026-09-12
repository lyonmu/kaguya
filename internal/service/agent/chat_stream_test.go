package agent

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"charm.land/fantasy"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
)

func TestChatStreamBlocks(t *testing.T) {
	var events []dtochat.ContentBlock
	s := newChatStream(func(b dtochat.ContentBlock) error { events = append(events, b); return nil })
	c := s.callbacks()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(c.OnReasoningStart("0", fantasy.ReasoningContent{}))
	must(c.OnReasoningDelta("0", "think "))
	must(c.OnReasoningDelta("0", "more"))
	must(c.OnReasoningEnd("0", fantasy.ReasoningContent{Text: "think more"}))
	must(c.OnTextStart("0"))
	must(c.OnTextDelta("0", "hello"))
	must(c.OnTextEnd("0"))
	must(c.OnToolInputStart("call-1", "search"))
	must(c.OnToolInputDelta("call-1", `{"q":`))
	must(c.OnToolInputDelta("call-1", `"go"}`))
	must(c.OnToolCall(fantasy.ToolCallContent{ToolCallID: "call-1", ToolName: "search", Input: `{"q":"go"}`, ProviderExecuted: true}))
	must(c.OnToolResult(fantasy.ToolResultContent{ToolCallID: "call-1", ToolName: "search", Result: fantasy.ToolResultOutputContentText{Text: "found"}, ProviderExecuted: true}))
	must(c.OnTextStart("0"))
	must(c.OnTextDelta("0", "answer"))
	must(c.OnTextEnd("0"))
	var blocks []dtochat.ContentBlock
	for _, event := range events {
		if event.Phase == dtochat.BlockPhaseEnd {
			blocks = append(blocks, event)
		}
	}
	if len(blocks) != 5 {
		t.Fatalf("block ends = %+v", blocks)
	}
	wantTypes := []dtochat.BlockType{dtochat.BlockTypeReasoning, dtochat.BlockTypeText, dtochat.BlockTypeToolCall, dtochat.BlockTypeToolResult, dtochat.BlockTypeText}
	for i, b := range blocks {
		if b.Type != wantTypes[i] || b.Phase != dtochat.BlockPhaseEnd {
			t.Fatalf("invalid block: %+v", b)
		}
	}
	if blocks[0].Text != "" || blocks[1].Text != "" || blocks[4].Text != "" || blocks[2].Input != `{"q":"go"}` {
		t.Fatalf("block end repeated content or lost tool input: %+v", blocks)
	}
	if blocks[2].ToolCallID != blocks[3].ToolCallID || blocks[3].Output.Text != "found" || !blocks[3].ProviderExecuted {
		t.Fatal("tool result lost association or output")
	}
	// 增量只包含当前片段，结束标记不再重复正文。
	if events[1].Text != "think " || events[2].Text != "more" || events[3].Text != "" || events[8].Input != `{"q":` {
		t.Fatalf("invalid deltas: %+v", events)
	}
	// 所有内容事件不再暴露块 ID / step_index；工具仍通过 tool_call_id 关联。
	for _, event := range events {
		data, err := json.Marshal(event)
		must(err)
		for _, field := range []string{`"id":`, `"step_index":`} {
			if strings.Contains(string(data), field) {
				t.Fatalf("unexpected %s in %s", field, data)
			}
		}
	}
	payload := dtochat.Chat{ID: "conv", Flag: dtochat.ChatFlagDelta, Block: &events[1]}
	data, err := json.Marshal(payload)
	must(err)
	var got dtochat.Chat
	must(json.Unmarshal(data, &got))
	if !reflect.DeepEqual(payload, got) {
		t.Fatalf("JSON roundtrip: %s", data)
	}
}

func TestChatStreamToolErrorAndMedia(t *testing.T) {
	var blocks []dtochat.ContentBlock
	s := newChatStream(func(b dtochat.ContentBlock) error { blocks = append(blocks, b); return nil })
	c := s.callbacks()
	err := c.OnToolCall(fantasy.ToolCallContent{ToolCallID: "bad", ToolName: "search", Input: "{", Invalid: true, ValidationError: errors.New("invalid input")})
	if err != nil {
		t.Fatal(err)
	}
	if !blocks[0].IsError || blocks[0].ErrorMessage != "invalid input" {
		t.Fatalf("lost validation error: %+v", blocks[0])
	}
	cases := []fantasy.ToolResultOutputContent{
		fantasy.ToolResultOutputContentError{Error: errors.New("tool failed")},
		&fantasy.ToolResultOutputContentText{Text: "ok"},
		&fantasy.ToolResultOutputContentMedia{Data: "YWJj", MediaType: "image/png", Text: "image"},
	}
	for _, result := range cases {
		if err := c.OnToolResult(fantasy.ToolResultContent{ToolCallID: string(result.GetType()), Result: result}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"text":"tool failed"`, `"is_error":true`, `"media_type":"image/png"`, `"data":"YWJj"`} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("missing %s in %s", field, data)
		}
	}
}

func TestChatStreamPropagatesSendError(t *testing.T) {
	expected := errors.New("disconnected")
	s := newChatStream(func(dtochat.ContentBlock) error { return expected })
	c := s.callbacks()
	if err := c.OnReasoningDelta("r", "thinking"); !errors.Is(err, expected) {
		t.Fatalf("got %v", err)
	}
	if err := c.OnToolCall(fantasy.ToolCallContent{ToolCallID: "t"}); !errors.Is(err, expected) {
		t.Fatalf("got %v", err)
	}
}

func TestChatStreamWithoutReasoning(t *testing.T) {
	var blocks []dtochat.ContentBlock
	s := newChatStream(func(b dtochat.ContentBlock) error { blocks = append(blocks, b); return nil })
	c := s.callbacks()
	if err := c.OnTextDelta("0", "answer"); err != nil {
		t.Fatal(err)
	}
	if err := c.OnTextEnd("0"); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || blocks[0].Type != dtochat.BlockTypeText || blocks[1].Text != "" {
		t.Fatalf("unexpected reasoning or repeated text: %+v", blocks)
	}
}
