package agent

import (
	"context"
	"time"

	"charm.land/fantasy"
	token "github.com/lyonmu/kaguya/internal/agent/token"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	memorysvc "github.com/lyonmu/kaguya/internal/service/memory"
)

// chatOutcome 保存生成结果或失败准备审计；只有成功结果进入 persist/done。
type chatOutcome struct {
	result       *fantasy.AgentResult
	trace        *turnTrace
	compactor    *contextCompactor
	preparation  *chatPreparation
	startedAt    time.Time
	finishedAt   time.Time
	paused       bool
	finishReason string
	usage        token.NormalizedUsage
}

func (o *chatOutcome) preparationAudit() *dtochat.TurnPreparationUsage {
	if o == nil || o.compactor == nil || o.compactor.preparationUsage == nil {
		return nil
	}
	copy := *o.compactor.preparationUsage
	return &copy
}

// referenceAudit 在解析成功后才非空；生成失败也保留已实际准备的安全元信息。
func (o *chatOutcome) referenceAudit() *dtochat.TurnReferenceSummary {
	if o == nil || o.preparation == nil {
		return nil
	}
	return o.preparation.referenceSummary
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

// instructionVersionPointer 只在版本非 0 时返回指针，区分“无 Manager”与版本 0。
func instructionVersionPointer(version int64) *int64 {
	if version <= 0 {
		return nil
	}
	return &version
}

// persist 在单个事务内写入完成轮次。失败时调用方不得发送 done；
// 提交成功后 Notify 唤醒记忆 Worker（通知丢失也不丢任务）。
func (o *chatOutcome) persist(ctx context.Context, exec chatExecution) error {
	target := exec.target
	requestPrompt := exec.prompt.requestPrompt
	var summary *dtochat.TurnReferenceSummary
	if o.preparation != nil {
		requestPrompt, summary = o.preparation.requestPrompt, o.preparation.referenceSummary
	}
	err := saveCompletedTurn(ctx, completedTurn{
		PreparationUsage:          o.preparationAudit(),
		ContextFingerprintVersion: contextFingerprintVersion,
		ContextFingerprint:        o.compactor.fingerprint.digest(),
		MemorySelection:           exec.prompt.memory.selection(),
		ProjectInstructions:       exec.prompt.projectSnapshot,
		InstructionsFormat:        exec.prompt.instructionsFormat,
		GlobalInstructionHash:     exec.prompt.globalHash,
		GlobalInstructionVersion:  instructionVersionPointer(exec.prompt.globalVersion),
		PermissionMode:            string(exec.permissionMode),
		ReferenceSummary:          summary,
		ConversationID:            exec.conversationID, ProjectID: exec.requestedProjectID, Version: exec.version,
		TurnID:      exec.turnID,
		UserContent: exec.userContent,
		ProviderID:  target.provider.ID, ProviderName: target.provider.ProviderName,
		ModelID: target.model.ModelID, ModelName: target.model.ModelName,
		APIProtocol: string(target.model.APIProtocol),
		StartedAt:   o.startedAt, FinishedAt: o.finishedAt, FinishReason: o.finishReason,
		Usage: o.usage, Messages: o.conversationMessages(requestPrompt), Blocks: o.trace.result(),
		ContextMessages: o.compactor.snapshot(o.result), CompactionCount: o.compactor.count,
		ContextTokens: completedResultContextTokens(o.result, o.paused), ContextWindow: target.model.TokenContextWindow,
	})
	if err != nil {
		return err
	}
	memorysvc.Notify()
	return nil
}
