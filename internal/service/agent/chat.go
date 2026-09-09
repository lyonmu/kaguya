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
	"github.com/lyonmu/kaguya/internal/ent/kaguyaproviderinfo"
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

// Chat 执行一次流式对话：查询所选模型（空值用默认）→ 组装 Agent → Stream 增量推送。
func (s *AgentSvc) Chat(ctx context.Context, dataChan chan *dtochat.ChatResp, req *dtochat.ChatReq) {
	defer close(dataChan)

	// 使用本地模型记录 ID，避免不同提供商相同 API 模型名冲突。
	query := db.EntClient.KaguyaModelsInfo.Query().
		Where(kaguyamodelsinfo.DeletedAtIsNil(), kaguyamodelsinfo.HasProviderWith(kaguyaproviderinfo.DeletedAtIsNil())).
		WithProvider()
	if req.ModelID != "" {
		query.Where(kaguyamodelsinfo.IDEQ(req.ModelID))
	} else {
		query.Where(kaguyamodelsinfo.IsDefault(consts.IsTrue))
	}
	model, err := query.First(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("query chat model failed, err is %+v", err)
		send(ctx, dataChan, &dtochat.ChatResp{Err: err})
		return
	}
	provider := model.Edges.Provider
	if provider == nil {
		err = fmt.Errorf("chat model %q has no provider", model.ModelID)
		global.Logger.Sugar().Error(err)
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
	release, err := acquireConversation(convID)
	if err != nil {
		send(ctx, dataChan, &dtochat.ChatResp{Err: err, Chat: dtochat.Chat{ID: convID, Flag: dtochat.WSFlagError}})
		return
	}
	defer release()
	history, version, err := loadConversation(ctx, convID)
	if err != nil {
		global.Logger.Sugar().Errorf("load conversation failed: %v", err)
		send(ctx, dataChan, &dtochat.ChatResp{Err: err, Chat: dtochat.Chat{ID: convID, Flag: dtochat.WSFlagError}})
		return
	}

	// 组装 Agent（每次请求新建）
	providerCfg := agentruntime.ProviderConfig{
		Name: provider.ProviderName, Type: provider.ProviderType, Protocol: consts.ProviderProtocol(provider.APIProtocol),
		BaseURL: provider.BaseURL, APIKey: provider.APIKey, ModelID: model.ModelID, ConversationID: convID,
	}
	ag, err := agentruntime.New(
		agentruntime.WithProvider(providerCfg),
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
	// 流式内容已发送后不能透明重试，否则失败尝试会混入同一轮展示/历史。
	maxRetries := 0
	call.MaxRetries = &maxRetries
	trace := newTurnTrace()
	trace.wrap(&call)
	call.Prompt = req.Messages
	call.Messages = history
	startedAt := time.Now()
	result, err := ag.Stream(streamCtx, call)
	finishedAt := time.Now()
	if err == nil {
		err = ctx.Err()
	}
	// 截断、过滤、未知终止也不算完整结束，不保存部分上下文。
	if err == nil && (result == nil || result.Response.FinishReason != fantasy.FinishReasonStop) {
		err = fmt.Errorf("conversation did not finish normally")
	}
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
	usage := token.FromFantasyUsage(result.TotalUsage)
	if err := trace.finish(finishedAt); err != nil {
		global.Logger.Sugar().Errorf("incomplete conversation trace: id=%s err=%v", convID, err)
		send(ctx, dataChan, &dtochat.ChatResp{Err: err, Chat: dtochat.Chat{ID: convID, Flag: dtochat.WSFlagError}})
		return
	}
	if err := saveCompletedTurn(ctx, completedTurn{
		ConversationID: convID, Version: version, UserContent: req.Messages,
		ProviderID: provider.ID, ProviderName: provider.ProviderName, ModelID: model.ModelID,
		ModelName: model.ModelName, APIProtocol: string(provider.APIProtocol),
		StartedAt: startedAt, FinishedAt: finishedAt, FinishReason: string(result.Response.FinishReason),
		Usage: usage, Messages: convMsgs, Blocks: trace.blocks,
	}); err != nil {
		global.Logger.Sugar().Errorf("persist completed conversation failed: id=%s err=%v", convID, err)
		send(ctx, dataChan, &dtochat.ChatResp{Err: err, Chat: dtochat.Chat{ID: convID, Flag: dtochat.WSFlagError}})
		return
	}

	// 事务提交后才发唯一 done；落库失败不能向前端报告本轮成功。
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
