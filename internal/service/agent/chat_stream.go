package agent

import (
	"encoding/json"
	"fmt"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/agent/permission"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
)

// chatStream 将 Fantasy 回调直接转换为前端事件，不缓存或重复发送正文。
// Fantasy 在消费流的同一个 goroutine 中同步调用这些回调。
type chatStream struct {
	mode permission.Mode
	emit func(dtochat.ContentBlock) error
}

func newChatStream(mode permission.Mode, emit func(dtochat.ContentBlock) error) *chatStream {
	return &chatStream{mode: mode, emit: emit}
}

func (s *chatStream) update(kind dtochat.BlockType, phase dtochat.BlockPhase, apply func(*dtochat.ContentBlock), delta string) error {
	event := dtochat.ContentBlock{Type: kind, Phase: phase}
	if apply != nil {
		apply(&event)
	}
	if phase == dtochat.BlockPhaseDelta {
		if kind == dtochat.BlockTypeToolCall {
			event.Input = delta
		} else {
			event.Text = delta
		}
	}
	return s.emit(event)
}

func (s *chatStream) callbacks() fantasy.AgentStreamCall {
	text := func(kind dtochat.BlockType, phase dtochat.BlockPhase, value string) error {
		return s.update(kind, phase, nil, value)
	}
	return fantasy.AgentStreamCall{
		OnTextStart: func(_ string) error { return text(dtochat.BlockTypeText, dtochat.BlockPhaseStart, "") },
		OnTextDelta: func(_ string, delta string) error { return text(dtochat.BlockTypeText, dtochat.BlockPhaseDelta, delta) },
		OnTextEnd:   func(_ string) error { return text(dtochat.BlockTypeText, dtochat.BlockPhaseEnd, "") },
		OnReasoningStart: func(_ string, _ fantasy.ReasoningContent) error {
			return text(dtochat.BlockTypeReasoning, dtochat.BlockPhaseStart, "")
		},
		OnReasoningDelta: func(_ string, delta string) error {
			return text(dtochat.BlockTypeReasoning, dtochat.BlockPhaseDelta, delta)
		},
		OnReasoningEnd: func(_ string, _ fantasy.ReasoningContent) error {
			return text(dtochat.BlockTypeReasoning, dtochat.BlockPhaseEnd, "")
		},
		OnToolInputStart: func(id, name string) error {
			return s.update(dtochat.BlockTypeToolCall, dtochat.BlockPhaseStart, func(b *dtochat.ContentBlock) { b.ToolCallID = id; b.ToolName = name }, "")
		},
		OnToolInputDelta: func(id, delta string) error {
			return s.update(dtochat.BlockTypeToolCall, dtochat.BlockPhaseDelta, func(b *dtochat.ContentBlock) { b.ToolCallID = id; b.Input += delta }, delta)
		},
		// OnToolCall 提供最终参数，作为工具输入块的 block_end 事件，不代表整轮结束。
		OnToolCall: func(call fantasy.ToolCallContent) error {
			// provider-executed 工具在本机没有 Run 可供审批；受限模式必须停止并报告，
			// 不能以事后回调伪称阻止副作用。
			if call.ProviderExecuted && !permission.RestrictedModeAllowsProviderTools(s.mode) {
				return fmt.Errorf("provider-executed tool %q is not supported in permission mode %s", call.ToolName, s.mode)
			}
			return s.update(dtochat.BlockTypeToolCall, dtochat.BlockPhaseEnd, func(b *dtochat.ContentBlock) {
				b.ToolCallID = call.ToolCallID
				b.ToolName = call.ToolName
				b.Input = call.Input
				b.ProviderExecuted = call.ProviderExecuted
				b.IsError = call.Invalid
				if call.ValidationError != nil {
					b.ErrorMessage = call.ValidationError.Error()
				}
			}, "")
		},
		OnToolResult: func(result fantasy.ToolResultContent) error {
			output, err := toolOutputWithMetadata(result)
			if err != nil {
				return err
			}
			return s.update(dtochat.BlockTypeToolResult, dtochat.BlockPhaseEnd, func(b *dtochat.ContentBlock) {
				b.ToolCallID = result.ToolCallID
				b.ToolName = result.ToolName
				b.ProviderExecuted = result.ProviderExecuted
				b.Output = output
				b.IsError = output != nil && output.Type == dtochat.ToolOutputError
			}, "")
		},
	}
}

// toolOutputWithMetadata 在通用输出映射之上补充审批结构化 code；工具元数据
// 只用于错误码判定，不改变正文。
func toolOutputWithMetadata(result fantasy.ToolResultContent) (*dtochat.ToolOutput, error) {
	output, err := toolOutput(result.Result)
	if err != nil || output == nil {
		return output, err
	}
	if code := approvalOutputCode(result.ClientMetadata); code != 0 {
		output.Code = code
		output.Type = dtochat.ToolOutputError
	}
	return output, nil
}

// approvalOutputCode 把 permission wrapper 写入的工具元数据映射为稳定错误码。
func approvalOutputCode(metadata string) int {
	if metadata == "" {
		return 0
	}
	var meta permission.Metadata
	if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
		return 0
	}
	switch permission.Outcome(meta.Outcome) {
	case permission.OutcomeRejected, permission.OutcomeCanceled:
		return dtocode.ApprovalDenied.Code
	case permission.OutcomeExpired:
		return dtocode.ApprovalExpired.Code
	default:
		return 0
	}
}

func toolOutput(result fantasy.ToolResultOutputContent) (*dtochat.ToolOutput, error) {
	if result == nil {
		return nil, nil
	}
	if v, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](result); ok {
		return &dtochat.ToolOutput{Type: dtochat.ToolOutputText, Text: v.Text}, nil
	}
	if v, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](result); ok {
		output := &dtochat.ToolOutput{Type: dtochat.ToolOutputError}
		if v.Error != nil {
			output.Text = v.Error.Error()
		}
		return output, nil
	}
	if v, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](result); ok {
		return &dtochat.ToolOutput{Type: dtochat.ToolOutputMedia, Text: v.Text, Data: v.Data, MediaType: v.MediaType}, nil
	}
	return nil, fmt.Errorf("unsupported tool output type %q", result.GetType())
}
