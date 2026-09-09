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

// ConversationContext 累计会话所有已完成轮次（不区分模型）的 token，使用最新轮次的模型窗口。
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
	var usage []struct {
		Total int64 `json:"total"`
	}
	err = db.EntClient.KaguyaChatTurn.Query().
		Where(kaguyachatturn.ConversationIDEQ(id), kaguyachatturn.TurnIndexLTE(turn.TurnIndex)).
		Aggregate(ent.As(ent.Sum(kaguyachatturn.FieldTotalTokens), "total")).Scan(ctx, &usage)
	if err != nil {
		return nil, err
	}
	total := usage[0].Total
	turn.ContextTokens = nil
	if total > 0 {
		turn.ContextTokens = &total
	}
	return contextResponse(turn), nil
}
