package agent

import (
	"fmt"
	"time"

	"charm.land/fantasy"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
)

// turnTrace 仅在内存收集，成功后整轮提交。内部模型块 ID 不进入实时 DTO。
// 工具按首次出现占位，结果回填原位置，绝不按结果返回先后来重排工具。
type turnTrace struct {
	step      int
	order     int64
	blocks    []dtochat.StoredBlock
	positions map[string]int
}

func newTurnTrace() *turnTrace { return &turnTrace{positions: make(map[string]int)} }
func (t *turnTrace) event(kind dtochat.BlockType, id string) *dtochat.StoredBlock {
	t.order++
	key := fmt.Sprintf("%d/%s/%s", t.step, kind, id)
	i, ok := t.positions[key]
	if !ok {
		i = len(t.blocks)
		t.positions[key] = i
		t.blocks = append(t.blocks, dtochat.StoredBlock{Sequence: int64(i + 1), Type: kind, StartedAt: time.Now(), StartOrder: t.order})
	}
	return &t.blocks[i]
}
func (t *turnTrace) end(b *dtochat.StoredBlock) { b.FinishedAt = time.Now(); b.EndOrder = t.order }
func (t *turnTrace) finish(at time.Time) error {
	for i := range t.blocks {
		if t.blocks[i].FinishedAt.IsZero() {
			if t.blocks[i].Type == dtochat.BlockTypeToolCall {
				return fmt.Errorf("tool call %q has no result", t.blocks[i].ToolCallID)
			}
			t.order++
			t.blocks[i].FinishedAt = at
			t.blocks[i].EndOrder = t.order
		}
	}
	return nil
}
func (t *turnTrace) wrap(c *fantasy.AgentStreamCall) {
	original := *c
	c.OnStepStart = func(step int) error {
		t.step = step
		if original.OnStepStart != nil {
			return original.OnStepStart(step)
		}
		return nil
	}
	c.OnTextStart = func(id string) error {
		t.event(dtochat.BlockTypeText, id)
		return original.OnTextStart(id)
	}
	c.OnTextDelta = func(id, delta string) error {
		t.event(dtochat.BlockTypeText, id).Text += delta
		return original.OnTextDelta(id, delta)
	}
	c.OnTextEnd = func(id string) error {
		t.end(t.event(dtochat.BlockTypeText, id))
		return original.OnTextEnd(id)
	}
	c.OnReasoningStart = func(id string, r fantasy.ReasoningContent) error {
		t.event(dtochat.BlockTypeReasoning, id)
		return original.OnReasoningStart(id, r)
	}
	c.OnReasoningDelta = func(id, delta string) error {
		t.event(dtochat.BlockTypeReasoning, id).Text += delta
		return original.OnReasoningDelta(id, delta)
	}
	c.OnReasoningEnd = func(id string, r fantasy.ReasoningContent) error {
		b := t.event(dtochat.BlockTypeReasoning, id)
		b.Text = r.Text
		t.end(b)
		return original.OnReasoningEnd(id, r)
	}
	c.OnToolInputStart = func(id, name string) error {
		b := t.event(dtochat.BlockTypeToolCall, id)
		b.ToolCallID, b.ToolName = id, name
		return original.OnToolInputStart(id, name)
	}
	c.OnToolInputDelta = func(id, delta string) error {
		b := t.event(dtochat.BlockTypeToolCall, id)
		b.ToolCallID = id
		b.Input += delta
		return original.OnToolInputDelta(id, delta)
	}
	c.OnToolCall = func(call fantasy.ToolCallContent) error {
		b := t.event(dtochat.BlockTypeToolCall, call.ToolCallID)
		b.ToolCallID, b.ToolName, b.Input = call.ToolCallID, call.ToolName, call.Input
		b.ProviderExecuted, b.IsError = call.ProviderExecuted, call.Invalid
		if call.ValidationError != nil {
			b.ErrorMessage = call.ValidationError.Error()
		}
		return original.OnToolCall(call)
	}
	c.OnToolResult = func(result fantasy.ToolResultContent) error {
		output, err := toolOutput(result.Result)
		if err != nil {
			return err
		}
		b := t.event(dtochat.BlockTypeToolCall, result.ToolCallID)
		b.ToolCallID, b.ToolName, b.Output = result.ToolCallID, result.ToolName, output
		b.ProviderExecuted = result.ProviderExecuted
		b.IsError = b.IsError || (output != nil && output.Type == dtochat.ToolOutputError)
		t.end(b)
		return original.OnToolResult(result)
	}
}
