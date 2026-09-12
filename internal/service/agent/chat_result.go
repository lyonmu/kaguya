package agent

import (
	"context"
	"time"

	"charm.land/fantasy"
	token "github.com/lyonmu/kaguya/internal/agent/token"
)

// chatOutcome 是一次成功轮次的结果；持久化与 done 帧都基于它，不重复计算。
type chatOutcome struct {
	result       *fantasy.AgentResult
	trace        *turnTrace
	compactor    *contextCompactor
	startedAt    time.Time
	finishedAt   time.Time
	paused       bool
	finishReason string
	usage        token.NormalizedUsage
}

// chatFinishReason 计算落库使用的结束原因；达到步骤上限而暂停时记为 step_limit。
func chatFinishReason(result *fantasy.AgentResult, paused bool) string {
	if paused {
		return "step_limit"
	}
	return string(result.Response.FinishReason)
}

// conversationMessages 还原本轮写入历史的模型上下文。
// step.Messages 只有模型与工具消息，不含 Prompt，因此必须同时保存用户提问。
func (o *chatOutcome) conversationMessages(userPrompt string) []fantasy.Message {
	messages := make([]fantasy.Message, 0, 1+len(o.result.Steps))
	messages = append(messages, fantasy.NewUserMessage(userPrompt))
	for _, step := range o.result.Steps {
		messages = append(messages, step.Messages...)
	}
	return messages
}

// persist 在单个事务内写入完成轮次。失败时调用方不得发送 done。
func (o *chatOutcome) persist(ctx context.Context, exec chatExecution) error {
	target := exec.target
	return saveCompletedTurn(ctx, completedTurn{
		AgentInstructions: &exec.prompt.instructions,
		ConversationID:    exec.conversationID, ProjectID: exec.requestedProjectID, Version: exec.version,
		TurnID:      exec.turnID,
		UserContent: exec.userContent,
		ProviderID:  target.provider.ID, ProviderName: target.provider.ProviderName,
		ModelID: target.model.ModelID, ModelName: target.model.ModelName,
		APIProtocol: string(target.provider.APIProtocol),
		StartedAt:   o.startedAt, FinishedAt: o.finishedAt, FinishReason: o.finishReason,
		Usage: o.usage, Messages: o.conversationMessages(exec.prompt.requestPrompt), Blocks: o.trace.result(),
		ContextMessages: o.compactor.snapshot(o.result), CompactionCount: o.compactor.count,
		ContextTokens: completedResultContextTokens(o.result, o.paused), ContextWindow: target.model.TokenContextWindow,
	})
}
