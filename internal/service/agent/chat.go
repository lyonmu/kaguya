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
func send(ctx context.Context, dataChan chan *dtochat.ChatResp, resp *dtochat.ChatResp) bool {
	select {
	case <-ctx.Done():
		return false
	case dataChan <- resp:
		return true
	}
}

// Chat 执行一次流式对话：查询默认模型 → 组装 Agent → Stream 增量推送。
func (s *AgentSvc) Chat(ctx context.Context, dataChan chan *dtochat.ChatResp, req *dtochat.ChatReq) {
	defer close(dataChan)

	// 查询默认模型及其 provider 配置
	model, err := db.EntClient.KaguyaModelsInfo.Query().
		Where(kaguyamodelsinfo.IsDefault(consts.IsTrue)).
		Where(kaguyamodelsinfo.DeletedAtIsNil()).
		WithProvider().
		First(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("query default model failed, err is %+v", err)
		send(ctx, dataChan, &dtochat.ChatResp{Err: err})
		return
	}
	provider := model.Edges.Provider
	if provider == nil {
		global.Logger.Sugar().Errorf("default model %q has no provider", model.ModelID)
		send(ctx, dataChan, &dtochat.ChatResp{Err: err})
		return
	}

	// 会话管理：仅新会话生成雪花 ID；同一 ID 用于历史查找及上游请求关联。
	convID := req.ID
	if convID == "" {
		id, gerr := global.Id.GenID()
		if gerr != nil {
			global.Logger.Sugar().Errorf("generate conversation id failed, err is %+v", gerr)
			send(ctx, dataChan, &dtochat.ChatResp{Err: gerr})
			return
		}
		convID = fmt.Sprintf("%d", id)
	}
	history := conversationStoreInstance.history(convID)

	// 组装 Agent（每次请求新建）
	ag, err := agentruntime.New(
		agentruntime.WithProvider(agentruntime.ProviderConfig{
			Name:           provider.ProviderName,
			Protocol:       consts.ProviderProtocol(provider.APIProtocol),
			BaseURL:        provider.BaseURL,
			APIKey:         provider.APIKey,
			ModelID:        model.ModelID,
			ConversationID: convID,
		}),
		agentruntime.WithSystemPrompt(chatSystemPrompt),
	)
	if err != nil {
		global.Logger.Sugar().Errorf("assemble agent failed, err is %+v", err)
		send(ctx, dataChan, &dtochat.ChatResp{Err: err})
		return
	}

	// 首条消息：携带会话 ID 与模型信息
	first := &dtochat.ChatResp{
		Chat:        dtochat.Chat{ID: convID, Flag: dtochat.WSFlagStart},
		APIProtocol: consts.ProviderProtocol(provider.APIProtocol),
		Created:     time.Now().Unix(),
		ModelID:     model.ModelID,
		ModelName:   model.ModelName,
	}
	if !send(ctx, dataChan, first) {
		return
	}

	// 流式执行对话
	streamCtx := token.WithConversationID(ctx, convID)
	stream := newChatStream(func(block dtochat.ContentBlock) error {
		chat := dtochat.Chat{ID: convID, Flag: dtochat.WSFlagDelta, Block: &block}
		if block.Type == dtochat.BlockTypeText && block.Phase == dtochat.BlockPhaseDelta {
			chat.Content = block.Text
		}
		if !send(ctx, dataChan, &dtochat.ChatResp{
			Chat:        chat,
			APIProtocol: consts.ProviderProtocol(provider.APIProtocol),
			Created:     time.Now().Unix(), ModelID: model.ModelID, ModelName: model.ModelName,
		}) {
			return ctx.Err()
		}
		return nil
	})
	call := stream.callbacks()
	call.Prompt = req.Messages
	call.Messages = history
	result, err := ag.Stream(streamCtx, call)
	if err != nil {
		global.Logger.Sugar().Errorf("stream chat failed, err is %+v", err)
		send(ctx, dataChan, &dtochat.ChatResp{Err: err, Chat: dtochat.Chat{ID: convID, Flag: dtochat.WSFlagError}})
		return
	}

	// step.Messages 只有模型/工具消息，不包含 Prompt；必须同时保存用户提问。
	convMsgs := make([]fantasy.Message, 0, 1+len(result.Steps))
	convMsgs = append(convMsgs, fantasy.NewUserMessage(req.Messages))
	for _, step := range result.Steps {
		convMsgs = append(convMsgs, step.Messages...)
	}
	conversationStoreInstance.append(convID, convMsgs...)

	// 唯一的整轮结束帧：仅 Usage，不重复发送已推送的正文/思考/工具内容
	usage := token.FromFantasyUsage(result.TotalUsage)
	global.Logger.Sugar().Infof("chat usage: conversation_id=%s input_tokens=%d output_tokens=%d total_tokens=%d reasoning_tokens=%d cached_tokens=%d",
		convID, usage.InputTokens, usage.OutputTokens, usage.TotalTokens, usage.ReasoningTokens, usage.CacheHitTokens)
	send(ctx, dataChan, &dtochat.ChatResp{
		Chat:        dtochat.Chat{ID: convID, Flag: dtochat.WSFlagDone},
		APIProtocol: consts.ProviderProtocol(provider.APIProtocol),
		Usage: dtochat.Usage{
			InputTokens:     int(usage.InputTokens),
			OutputTokens:    int(usage.OutputTokens),
			TotalTokens:     int(usage.TotalTokens),
			CachedTokens:    int(usage.CacheHitTokens),
			ReasoningTokens: int(usage.ReasoningTokens),
		},
		Created:   time.Now().Unix(),
		ModelID:   model.ModelID,
		ModelName: model.ModelName,
	})
}
