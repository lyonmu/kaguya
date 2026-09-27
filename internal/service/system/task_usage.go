package system

import (
	"context"
	"time"

	agentruntime "github.com/lyonmu/kaguya/internal/agent/runtime"
	token "github.com/lyonmu/kaguya/internal/agent/token"
	"github.com/lyonmu/kaguya/internal/ent"
)

// taskUsageRecorder 只持有计量元数据，不持有 API key 或请求内容。
type taskUsageRecorder struct {
	client                                                             *ent.Client
	kind, conversationID, providerID, providerName, modelID, modelName string
}

func NewTaskUsageRecorder(client *ent.Client, kind string, cfg agentruntime.ProviderConfig) token.UsageRecorder {
	modelID, name := cfg.ModelRecordID, cfg.ModelName
	if modelID == "" {
		modelID = "upstream:" + cfg.ModelID
	}
	if name == "" {
		name = cfg.ModelID
	}
	return &taskUsageRecorder{client: client, kind: kind, conversationID: cfg.ConversationID,
		providerID: cfg.ProviderID, providerName: cfg.Name, modelID: modelID, modelName: name}
}

func (r *taskUsageRecorder) RecordUsage(ctx context.Context, usage token.TurnUsage) error {
	// 取消/超时也可能产生消费；保存不依赖已取消的调用 context。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	u := usage.Total
	return r.client.KaguyaTaskUsage.Create().SetKind(r.kind).SetConversationID(r.conversationID).
		SetProviderID(r.providerID).SetProviderName(r.providerName).SetModelID(r.modelID).SetModelName(r.modelName).
		SetFinishedAt(usage.FinishedAt.UTC()).SetUsageKnown(usage.UsageKnown || u.TotalTokens > 0).
		SetInputTokens(u.InputTokens).SetOutputTokens(u.OutputTokens).SetReasoningTokens(u.ReasoningTokens).
		SetCachedTokens(u.CacheHitTokens).SetTotalTokens(u.TotalTokens).Exec(ctx)
}
