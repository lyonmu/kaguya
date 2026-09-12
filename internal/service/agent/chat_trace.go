package agent

import (
	"fmt"
	"sync"
	"time"

	"charm.land/fantasy"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
)

// turnTrace 在生成过程中收集内容块；成功提交前也供增量落库并发读取，因此必须加锁。
// 工具按首次出现占位，结果回填原位置，绝不按结果返回先后来重排工具。
type turnTrace struct {
	mu        sync.Mutex
	step      int
	order     int64
	revision  int64
	toolRev   int64
	bytes     int64
	blocks    []dtochat.StoredBlock
	revs      []int64 // 与 blocks 平行，每块最后一次变更版本
	positions map[string]int
}

func newTurnTrace() *turnTrace { return &turnTrace{positions: make(map[string]int)} }

// event 创建或返回块；调用方必须持有 t.mu。
func (t *turnTrace) event(kind dtochat.BlockType, id string) *dtochat.StoredBlock {
	t.order++
	key := fmt.Sprintf("%d/%s/%s", t.step, kind, id)
	i, ok := t.positions[key]
	if !ok {
		i = len(t.blocks)
		t.positions[key] = i
		t.blocks = append(t.blocks, dtochat.StoredBlock{Sequence: int64(i + 1), Type: kind, StartedAt: time.Now(), StartOrder: t.order})
		t.revs = append(t.revs, 0)
	}
	t.revision++
	t.revs[i] = t.revision
	return &t.blocks[i]
}

// end 标记块结束；调用方必须持有 t.mu。
func (t *turnTrace) end(b *dtochat.StoredBlock) {
	b.FinishedAt = time.Now()
	b.EndOrder = t.order
}

// finish 把所有未结束的块补上结束时间。工具调用没有结果说明轨迹不完整，拒绝提交。
func (t *turnTrace) finish(at time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.blocks {
		if t.blocks[i].FinishedAt.IsZero() {
			if t.blocks[i].Type == dtochat.BlockTypeToolCall {
				return fmt.Errorf("tool call %q has no result", t.blocks[i].ToolCallID)
			}
			t.order++
			t.blocks[i].FinishedAt = at
			t.blocks[i].EndOrder = t.order
			t.revision++
			t.revs[i] = t.revision
		}
	}
	return nil
}

// stats 返回当前累计内容字节、版本号与工具事件版本号，供增量落库判断是否需要刷盘。
func (t *turnTrace) stats() (bytes, revision, toolRevision int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bytes, t.revision, t.toolRev
}

// snapshot 返回内容块的只读副本及各自版本，调用方可安全落库。
func (t *turnTrace) snapshot() []traceBlock {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]traceBlock, len(t.blocks))
	for i := range t.blocks {
		out[i] = traceBlock{Block: t.blocks[i], Revision: t.revs[i]}
	}
	return out
}

// result 返回成功提交所需的块副本。
func (t *turnTrace) result() []dtochat.StoredBlock {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]dtochat.StoredBlock, len(t.blocks))
	copy(out, t.blocks)
	return out
}

// traceBlock 是 snapshot 的元素：块的当前值与变更版本。
type traceBlock struct {
	Block    dtochat.StoredBlock
	Revision int64
}

func (t *turnTrace) wrap(c *fantasy.AgentStreamCall) {
	original := *c
	c.OnStepStart = func(step int) error {
		t.mu.Lock()
		t.step = step
		t.mu.Unlock()
		if original.OnStepStart != nil {
			return original.OnStepStart(step)
		}
		return nil
	}
	c.OnTextStart = func(id string) error {
		t.mu.Lock()
		t.event(dtochat.BlockTypeText, id)
		t.mu.Unlock()
		return original.OnTextStart(id)
	}
	c.OnTextDelta = func(id, delta string) error {
		t.mu.Lock()
		b := t.event(dtochat.BlockTypeText, id)
		b.Text += delta
		t.bytes += int64(len(delta))
		t.mu.Unlock()
		return original.OnTextDelta(id, delta)
	}
	c.OnTextEnd = func(id string) error {
		t.mu.Lock()
		t.end(t.event(dtochat.BlockTypeText, id))
		t.mu.Unlock()
		return original.OnTextEnd(id)
	}
	c.OnReasoningStart = func(id string, r fantasy.ReasoningContent) error {
		t.mu.Lock()
		t.event(dtochat.BlockTypeReasoning, id)
		t.mu.Unlock()
		return original.OnReasoningStart(id, r)
	}
	c.OnReasoningDelta = func(id, delta string) error {
		t.mu.Lock()
		b := t.event(dtochat.BlockTypeReasoning, id)
		b.Text += delta
		t.bytes += int64(len(delta))
		t.mu.Unlock()
		return original.OnReasoningDelta(id, delta)
	}
	c.OnReasoningEnd = func(id string, r fantasy.ReasoningContent) error {
		t.mu.Lock()
		b := t.event(dtochat.BlockTypeReasoning, id)
		t.bytes += int64(len(r.Text)) - int64(len(b.Text))
		b.Text = r.Text
		t.end(b)
		t.mu.Unlock()
		return original.OnReasoningEnd(id, r)
	}
	c.OnToolInputStart = func(id, name string) error {
		t.mu.Lock()
		b := t.event(dtochat.BlockTypeToolCall, id)
		b.ToolCallID, b.ToolName = id, name
		t.toolRev++
		t.mu.Unlock()
		return original.OnToolInputStart(id, name)
	}
	c.OnToolInputDelta = func(id, delta string) error {
		t.mu.Lock()
		b := t.event(dtochat.BlockTypeToolCall, id)
		b.ToolCallID = id
		b.Input += delta
		t.bytes += int64(len(delta))
		t.toolRev++
		t.mu.Unlock()
		return original.OnToolInputDelta(id, delta)
	}
	c.OnToolCall = func(call fantasy.ToolCallContent) error {
		t.mu.Lock()
		b := t.event(dtochat.BlockTypeToolCall, call.ToolCallID)
		t.bytes += int64(len(call.Input)) - int64(len(b.Input))
		b.ToolCallID, b.ToolName, b.Input = call.ToolCallID, call.ToolName, call.Input
		b.ProviderExecuted, b.IsError = call.ProviderExecuted, call.Invalid
		if call.ValidationError != nil {
			b.ErrorMessage = call.ValidationError.Error()
		}
		t.toolRev++
		t.mu.Unlock()
		return original.OnToolCall(call)
	}
	c.OnToolResult = func(result fantasy.ToolResultContent) error {
		output, err := toolOutput(result.Result)
		if err != nil {
			return err
		}
		t.mu.Lock()
		b := t.event(dtochat.BlockTypeToolCall, result.ToolCallID)
		if output != nil {
			t.bytes += int64(len(output.Text))
		}
		b.ToolCallID, b.ToolName, b.Output = result.ToolCallID, result.ToolName, output
		b.ProviderExecuted = result.ProviderExecuted
		b.IsError = b.IsError || (output != nil && output.Type == dtochat.ToolOutputError)
		t.end(b)
		t.toolRev++
		t.mu.Unlock()
		return original.OnToolResult(result)
	}
}
