package agent

import (
	"context"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
)

// 最后一次调用的输入已含整个历史和本轮工具消息；不能使用 TotalUsage 累加值。
// 输出中的 reasoning 不重复计入。供应商没有报告 usage 时保留未知。
func completedContextTokens(usage fantasy.Usage) *int64 {
	tokens := max(usage.InputTokens, 0) + max(usage.CacheReadTokens, 0) + max(usage.CacheCreationTokens, 0) + max(usage.OutputTokens, 0)
	if tokens == 0 {
		return nil
	}
	return &tokens
}

// A paused tool step has results not yet counted by a provider's next input usage.
// Estimate only these new tool messages; Response may point to an earlier text step.
func completedResultContextTokens(result *fantasy.AgentResult, paused bool) *int64 {
	usage := result.Response.Usage
	var messages []fantasy.Message
	if len(result.Steps) > 0 {
		last := result.Steps[len(result.Steps)-1]
		usage, messages = last.Usage, last.Messages
	}
	tokens := completedContextTokens(usage)
	if paused && tokens != nil {
		for _, message := range messages {
			if message.Role == fantasy.MessageRoleTool {
				*tokens += estimateMessages([]fantasy.Message{message})
			}
		}
	}
	return tokens
}

func contextResponse(turn *ent.KaguyaChatTurn) *dtochat.ConversationContextResp {
	resp := &dtochat.ConversationContextResp{ConversationID: turn.ConversationID, TurnIndex: turn.TurnIndex, ModelID: turn.ModelID, ModelName: turn.ModelName,
		ContextTokens: turn.ContextTokens, ContextWindow: turn.ContextWindow, EffectiveWindow: int64(turn.ContextWindow) * 9 / 10, WindowRatio: 0.9}
	if turn.ContextTokens != nil && resp.EffectiveWindow > 0 {
		percent := float64(*turn.ContextTokens) / float64(resp.EffectiveWindow) * 100
		maxPercent := float64(*turn.ContextTokens) / float64(turn.ContextWindow) * 100
		resp.Percent, resp.MaxWindowPercent = &percent, &maxPercent
	}
	return resp
}

// ConversationContext 使用最新完成轮次的实际上下文占用和模型窗口，不累计计费用量。
func (s *AgentSvc) ConversationContext(ctx context.Context, id string) (*dtochat.ConversationContextResp, error) {
	turn, err := db.EntClient.KaguyaChatTurn.Query().
		Where(kaguyachatturn.ConversationIDEQ(id), kaguyachatturn.HasConversationWith(kaguyaconversation.DeletedAtIsNil())).
		Select(kaguyachatturn.FieldConversationID, kaguyachatturn.FieldTurnIndex, kaguyachatturn.FieldModelID, kaguyachatturn.FieldModelName, kaguyachatturn.FieldContextTokens, kaguyachatturn.FieldContextWindow).
		Order(ent.Desc(kaguyachatturn.FieldTurnIndex)).First(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrConversationNotFound
	}
	if err != nil {
		return nil, err
	}
	return contextResponse(turn), nil
}

// Reserve the final 10% for generation and respect a lower configured output limit.
func contextOutputLimit(window, configured int) *int64 {
	limit := int64(configured)
	if window > 0 {
		reserve := max(int64(window)/10, 1)
		if limit <= 0 || reserve < limit {
			limit = reserve
		}
	}
	if limit <= 0 {
		return nil
	}
	return &limit
}
