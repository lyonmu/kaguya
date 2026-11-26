package agent

import (
	"context"
	"fmt"

	"charm.land/fantasy"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
)

// chatPreparation 只由串行 PrepareStep 写入，生成结束后才交给事务保存。
// frozen chatPrompt 的 system/instructions/工具与原始请求保持不变。
type chatPreparation struct {
	initialized      bool
	requestPrompt    string
	referenceSummary *dtochat.TurnReferenceSummary
}

// prepareChatStep 先压缩必要历史，再解析一次引用，最后附加 transient memory。
// opts.Model 是当前模型；不获取另一底层模型，也不创建第二个 compactor。
func prepareChatStep(c *contextCompactor, prompt *chatPrompt, state *chatPreparation) fantasy.PrepareStepFunction {
	return func(ctx context.Context, opts fantasy.PrepareStepFunctionOptions) (context.Context, fantasy.PrepareStepResult, error) {
		if err := ctx.Err(); err != nil {
			return ctx, fantasy.PrepareStepResult{}, err
		}
		nextCtx, prepared, err := c.prepare(ctx, opts)
		if err != nil {
			return nextCtx, prepared, err
		}
		messages := prepared.Messages
		if messages == nil {
			messages = append([]fantasy.Message(nil), opts.Messages...)
		}
		if !state.initialized {
			if len(prompt.references) > 0 {
				budget := referenceByteBudget(c, messages)
				resolution, err := resolveReferenceContextWithBudget(ctx, prompt.projectID, prompt.references, budget, c.window > 0)
				if err != nil {
					return nextCtx, fantasy.PrepareStepResult{}, err
				}
				// 首个 step 最后一个消息必须是当前用户输入，不能修改历史里的同名提问。
				index := len(messages) - 1
				if index < 0 || messages[index].Role != fantasy.MessageRoleUser {
					return nextCtx, fantasy.PrepareStepResult{}, fmt.Errorf("current user message is unavailable during reference preparation")
				}
				state.requestPrompt = prompt.requestPrompt + resolution.prompt
				state.referenceSummary = resolution.summary
				messages = append([]fantasy.Message(nil), messages...)
				message := messages[index]
				message.Content = fantasy.NewUserMessage(state.requestPrompt).Content
				messages[index] = message
				// 更新私有续聊副本；seen 仍使用 Fantasy 原序列长度，不能按压缩后的长度改写。
				c.messages = messages
				if c.window <= 0 {
					c.seen = len(opts.Messages)
				}
				if c.window > 0 && estimateMessages(messages)+c.toolTokens+c.transientTokens >= c.thresholdTokens() {
					return nextCtx, fantasy.PrepareStepResult{}, ErrContextBudgetExceeded
				}
			}
			state.initialized = true
		} else if c.window <= 0 && len(prompt.references) > 0 {
			// 窗口未知仍需保留首步引用，Fantasy 后续 opts 追加的是原始 prompt 序列。
			if c.seen > len(opts.Messages) {
				return nextCtx, fantasy.PrepareStepResult{}, fmt.Errorf("context message sequence regressed")
			}
			c.messages = append(c.messages, opts.Messages[c.seen:]...)
			c.seen = len(opts.Messages)
			messages = c.messages
		}
		if err := ctx.Err(); err != nil {
			return nextCtx, fantasy.PrepareStepResult{}, err
		}
		prepared.Messages = InjectMemory(messages, prompt.memory.renderTransient(ctx))
		if err := ctx.Err(); err != nil {
			return nextCtx, fantasy.PrepareStepResult{}, err
		}
		return nextCtx, prepared, nil
	}
}
