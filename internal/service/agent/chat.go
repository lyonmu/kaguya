package agent

import (
	"context"
	"fmt"
	"time"

	"charm.land/fantasy"

	agentruntime "github.com/lyonmu/kaguya/internal/agent/runtime"
	token "github.com/lyonmu/kaguya/internal/agent/token"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamodelsinfo"
	"github.com/lyonmu/kaguya/internal/global"
)

// chatSystemPrompt 组装 Agent 时使用的系统提示词。
const chatSystemPrompt = "你是一个乐于助人的 AI 助手。"

// send 向 dataChan 推送一条消息；客户端已断开（ctx 取消）或 channel 已关闭时返回 false。
func send(ctx context.Context, dataChan chan *dtochat.ChatSSEResp, resp *dtochat.ChatSSEResp) bool {
	select {
	case <-ctx.Done():
		return false
	case dataChan <- resp:
		return true
	}
}

// Chat 执行一次流式对话：查询默认模型 → 组装 Agent → Stream 增量推送。
func (s *AgentSvc) Chat(ctx context.Context, dataChan chan *dtochat.ChatSSEResp, req *dtochat.ChatReq) {
	defer close(dataChan)

	// 查询默认模型及其 provider 配置
	model, err := db.EntClient.KaguyaModelsInfo.Query().
		Where(kaguyamodelsinfo.IsDefault(consts.IsTrue)).
		Where(kaguyamodelsinfo.DeletedAtIsNil()).
		WithProvider().
		First(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("query default model failed, err is %+v", err)
		send(ctx, dataChan, &dtochat.ChatSSEResp{Err: err})
		return
	}
	provider := model.Edges.Provider
	if provider == nil {
		global.Logger.Sugar().Errorf("default model %q has no provider", model.ModelID)
		send(ctx, dataChan, &dtochat.ChatSSEResp{Err: err})
		return
	}

	// 组装 Agent（每次请求新建）
	ag, err := agentruntime.New(
		agentruntime.WithProvider(agentruntime.ProviderConfig{
			Name:     provider.ProviderName,
			Protocol: consts.ProviderProtocol(provider.APIProtocol),
			BaseURL:  provider.BaseURL,
			APIKey:   provider.APIKey,
			ModelID:  model.ModelID,
		}),
		agentruntime.WithSystemPrompt(chatSystemPrompt),
	)
	if err != nil {
		global.Logger.Sugar().Errorf("assemble agent failed, err is %+v", err)
		send(ctx, dataChan, &dtochat.ChatSSEResp{Err: err})
		return
	}

	// 会话管理：空 ID 生成新会话，否则沿用历史
	convID := req.ConversationID
	if convID == "" {
		id, gerr := global.Id.GenID()
		if gerr != nil {
			global.Logger.Sugar().Errorf("generate conversation id failed, err is %+v", gerr)
			send(ctx, dataChan, &dtochat.ChatSSEResp{Err: gerr})
			return
		}
		convID = fmt.Sprintf("%d", id)
	}
	history := conversationStoreInstance.history(convID)

	// 首条消息：携带会话 ID 与模型信息
	first := &dtochat.ChatSSEResp{
		Chat:        dtochat.Chat{ID: convID},
		APIProtocol: consts.ProviderProtocol(provider.APIProtocol),
		Created:     time.Now().Unix(),
		ModelID:     model.ModelID,
		ModelName:   model.ModelName,
	}
	if !send(ctx, dataChan, first) {
		return
	}

	// 流式执行对话
	streamCtx := token.WithConversationID(token.WithMessageID(ctx, convID+"-"+time.Now().Format("150405")), convID)
	result, err := ag.Stream(streamCtx, fantasy.AgentStreamCall{
		Prompt:   req.Messages,
		Messages: history,
		OnTextDelta: func(_ string, delta string) error {
			if !send(ctx, dataChan, &dtochat.ChatSSEResp{
				Chat:        dtochat.Chat{ID: convID, Content: delta},
				APIProtocol: consts.ProviderProtocol(provider.APIProtocol),
				Created:     time.Now().Unix(),
				ModelID:     model.ModelID,
				ModelName:   model.ModelName,
			}) {
				return ctx.Err()
			}
			return nil
		},
	})
	if err != nil {
		global.Logger.Sugar().Errorf("stream chat failed, err is %+v", err)
		send(ctx, dataChan, &dtochat.ChatSSEResp{Err: err, Chat: dtochat.Chat{ID: convID}})
		return
	}

	// 该轮消息追加进历史（与 conversation_test.go 的 appendHistory 一致）
	convMsgs := make([]fantasy.Message, 0, len(result.Steps))
	for _, step := range result.Steps {
		convMsgs = append(convMsgs, step.Messages...)
	}
	conversationStoreInstance.append(convID, convMsgs...)

	// 末条消息：完整回答 + Usage
	usage := token.FromFantasyUsage(result.TotalUsage)
	send(ctx, dataChan, &dtochat.ChatSSEResp{
		Chat:        dtochat.Chat{ID: convID, Content: result.Response.Content.Text()},
		APIProtocol: consts.ProviderProtocol(provider.APIProtocol),
		Usage: dtochat.Usage{
			InputTokens:  int(usage.InputTokens),
			OutputTokens: int(usage.OutputTokens),
			TotalTokens:  int(usage.TotalTokens),
			CachedTokens: int(usage.CacheHitTokens),
		},
		Created:   time.Now().Unix(),
		ModelID:   model.ModelID,
		ModelName: model.ModelName,
	})
}
