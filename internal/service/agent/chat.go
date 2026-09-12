package agent

import (
	"context"
	"errors"
	"time"

	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/global"
)

// send 向 dataChan 推送一条消息；客户端已断开（ctx 取消）或 channel 已关闭时返回 false。
func send(ctx context.Context, dataChan chan *dtochat.ChatResp, resp *dtochat.ChatResp) bool {
	select {
	case <-ctx.Done():
		return false
	case dataChan <- resp:
		return true
	}
}

// ErrChatModelNotConfigured 表示未指定模型且系统配置无默认模型；
// API 层据其返回固定的用户可读提示。
var ErrChatModelNotConfigured = errors.New("chat model is not configured")

// pushChatError 推送错误帧并结束本轮。
// convID 为空表示会话尚未建立，此时不下发会话标识；否则前端据其定位失败的会话。
func pushChatError(ctx context.Context, dataChan chan *dtochat.ChatResp, convID string, err error) {
	resp := &dtochat.ChatResp{Err: err}
	if convID != "" {
		resp.Chat = dtochat.Chat{ID: convID, Flag: dtochat.WSFlagError}
	}
	send(ctx, dataChan, resp)
}

// startFrame 是本轮下发的第一条帧，携带会话 ID 与模型信息。
func startFrame(exec chatExecution) *dtochat.ChatResp {
	provider, model := exec.target.provider, exec.target.model
	return &dtochat.ChatResp{
		Chat:        dtochat.Chat{ID: exec.conversationID, Flag: dtochat.WSFlagStart},
		APIProtocol: consts.ProviderProtocol(provider.APIProtocol),
		Created:     time.Now().Unix(),
		ModelID:     model.ModelID,
		ModelName:   model.ModelName,
	}
}

// doneFrame 是本轮唯一的成功终止帧，只在事务提交后下发。
func doneFrame(exec chatExecution, outcome *chatOutcome) *dtochat.ChatResp {
	provider, model, usage := exec.target.provider, exec.target.model, outcome.usage
	return &dtochat.ChatResp{
		Chat:         dtochat.Chat{ID: exec.conversationID, Flag: dtochat.WSFlagDone},
		FinishReason: outcome.finishReason,
		APIProtocol:  consts.ProviderProtocol(provider.APIProtocol),
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
	}
}

// Chat 执行一次流式对话。整体分为四步，任一步失败都不落库：
//
//	1. 解析目标模型与提供商；
//	2. 建立会话身份（互斥 + 并发上限）并读取历史；
//	3. 组装工作区、提示词与 Agent；
//	4. 流式生成，成功后在同一事务写入轮次并下发 done。
func (s *AgentSvc) Chat(ctx context.Context, dataChan chan *dtochat.ChatResp, req *dtochat.ChatReq) {
	defer beginWork()()
	defer close(dataChan)

	target, err := resolveChatTarget(ctx, req.ModelID)
	if err != nil {
		pushChatError(ctx, dataChan, "", err)
		return
	}

	// 仅新会话生成雪花 ID；同一 ID 用于历史查找及上游请求关联。
	convID, err := newConversationID(req.ID)
	if err != nil {
		global.Logger.Sugar().Errorf("generate conversation id failed, err is %+v", err)
		pushChatError(ctx, dataChan, "", err)
		return
	}
	release, err := acquireConversation(convID)
	if err != nil {
		pushChatError(ctx, dataChan, convID, err)
		return
	}
	defer release()
	if !tryAcquire(chatSlots) {
		pushChatError(ctx, dataChan, convID, ErrChatConcurrencyLimited)
		return
	}
	defer releaseSlot(chatSlots)

	row, history, err := loadConversationRow(ctx, convID)
	if err != nil {
		global.Logger.Sugar().Errorf("load conversation failed: %v", err)
		pushChatError(ctx, dataChan, convID, err)
		return
	}
	version := int64(0)
	// 续聊的工作区来自数据库中的项目归属，不接受请求覆盖；
	// 首次失败后留下但尚无轮次的会话同样以数据库记录为准。
	projectID := req.ProjectID
	if row != nil {
		version = row.TurnCount
		projectID = ""
		if row.ProjectID != nil {
			projectID = *row.ProjectID
		}
	}
	toolset, err := s.projectTools(ctx, convID, projectID, version)
	if err != nil {
		global.Logger.Sugar().Warnf("prepare project tools failed: conversation_id=%s err=%v", convID, err)
		pushChatError(ctx, dataChan, convID, err)
		return
	}
	if toolset != nil {
		defer closeToolset(toolset)
	}

	// 首轮在正文开始生成前立即落库，新会话无需等待完成就出现在列表中；
	// 标题只用用户提问并发生成并异步更新，失败或取消的轮次留下空会话。
	if row == nil {
		if err := createConversation(ctx, target, convID, projectID, time.Now()); err != nil {
			global.Logger.Sugar().Errorf("create conversation failed: id=%s err=%v", convID, err)
			pushChatError(ctx, dataChan, convID, err)
			return
		}
		startEarlyTitleTask(ctx, convID, req.Messages)
	}

	prompt, err := prepareChatPrompt(ctx, target, toolset, convID, req)
	if err != nil {
		pushChatError(ctx, dataChan, "", err)
		return
	}
	exec := chatExecution{
		target: target, conversationID: convID, version: version, history: history, prompt: prompt,
		requestedProjectID: req.ProjectID, userContent: req.Messages,
	}

	agent, err := buildChatAgent(exec)
	if err != nil {
		global.Logger.Sugar().Errorf("assemble agent failed, err is %+v", err)
		pushChatError(ctx, dataChan, "", err)
		return
	}
	if !send(ctx, dataChan, startFrame(exec)) {
		return
	}

	outcome, err := s.streamChat(ctx, dataChan, agent, exec)
	if err != nil {
		global.Logger.Sugar().Errorf("stream chat failed, err is %+v", err)
		pushChatError(ctx, dataChan, convID, err)
		return
	}
	if err := outcome.persist(ctx, exec); err != nil {
		global.Logger.Sugar().Errorf("persist completed conversation failed: id=%s err=%v", convID, err)
		pushChatError(ctx, dataChan, convID, err)
		return
	}

	usage := outcome.usage
	global.Logger.Sugar().Infof("chat usage: conversation_id=%s input_tokens=%d output_tokens=%d total_tokens=%d reasoning_tokens=%d cached_tokens=%d",
		convID, usage.InputTokens, usage.OutputTokens, usage.TotalTokens, usage.ReasoningTokens, usage.CacheHitTokens)
	send(ctx, dataChan, doneFrame(exec, outcome))
}
