package chat

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/global"
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
)

func conversationFailure(c *gin.Context, err error, fallback dtocode.Response) {
	// 客户端取消过期请求或断开连接是正常生命周期，不再写响应或记录服务异常。
	// 同时检查请求 context，避免吞掉服务内部独立任务的取消错误。
	if errors.Is(err, context.Canceled) && errors.Is(c.Request.Context().Err(), context.Canceled) {
		return
	}
	switch {
	case errors.Is(err, serviceagent.ErrTaskModelNotConfigured):
		dtocode.TaskModelNotConfigured.Failure(c)
	case errors.Is(err, serviceagent.ErrConversationNotFound):
		dtocode.ConversationNotFound.Failure(c)
	case errors.Is(err, serviceagent.ErrConversationBusy):
		dtocode.ChatWSBusy.Failure(c)
	case errors.Is(err, serviceagent.ErrConversationUpdate):
		dtocode.RequestParameterError.Failure(c)
	default:
		global.Logger.Sugar().Errorf("conversation API failed: %v", err)
		fallback.Failure(c)
	}
}

// ConversationPage
// @Tags Chat History
// @Summary 会话列表（仅已完成会话）
// @Description 按最近完整轮次时间倒序；支持标题前缀搜索、收藏筛选。新建会话仍使用 SSE/WS 空 id 请求，首次成功后才出现在列表中。
// @Param data query dtochat.ConversationPageReq true "分页/筛选"
// @Success 200 {object} dtocode.Response{data=dtochat.ConversationListResp}
// @Router /v1/chat/conversation/page [get]
func (b *ChatApiV1Group) ConversationPage(c *gin.Context) {
	var req dtochat.ConversationPageReq
	if err := c.ShouldBindQuery(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationPage(c.Request.Context(), &req)
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationQueryFailure)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// ConversationDetail
// @Tags Chat History
// @Summary 会话摘要与累计运行信息（Overview）
// @Param id path string true "会话雪花 ID"
// @Success 200 {object} dtocode.Response{data=dtochat.ConversationResp}
// @Router /v1/chat/conversation/{id} [get]
func (b *ChatApiV1Group) ConversationDetail(c *gin.Context) {
	var uri dtochat.ConversationIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationDetail(c.Request.Context(), uri.ID)
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationQueryFailure)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// ConversationTitleWait
// @Tags Chat History
// @Summary 等待已有标题任务并返回已保存标题
// @Description 兼容等待接口，不启动生成任务；无任务时直接返回当前标题。仅等待当前进程的任务，最多30秒。新客户端应使用 POST 接口生成标题。不存在或已删除会话返回会话不存在。
// @Param id path string true "会话雪花 ID"
// @Success 200 {object} dtocode.Response{data=dtochat.ConversationTitleResp}
// @Router /v1/chat/conversation/{id}/title/wait [get]
func (b *ChatApiV1Group) ConversationTitleWait(c *gin.Context) {
	var uri dtochat.ConversationIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationTitleWait(c.Request.Context(), uri.ID)
	if c.Request.Context().Err() != nil {
		return
	}
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationQueryFailure)
		return
	}
	c.Header("Cache-Control", "no-store")
	dtocode.SystemSuccess.Success(resp, c)
}

// ConversationTitleGenerate
// @Tags Chat History
// @Summary 默认标题生成或重试，并等待已保存标题
// @Description 每轮成功结束且标题仍为“新对话”时调用一次。使用全局任务模型根据已保存首轮问答生成标题，未配置任务模型返回 102007。已有任务则等待，不覆盖非默认标题。最多等待30秒，生成失败或等待超时返回当前标题，不自动重试；聊天主流程不自动生成标题。
// @Param id path string true "会话雪花 ID"
// @Success 200 {object} dtocode.Response{data=dtochat.ConversationTitleResp}
// @Router /v1/chat/conversation/{id}/title/wait [post]
func (b *ChatApiV1Group) ConversationTitleGenerate(c *gin.Context) {
	var uri dtochat.ConversationIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationTitleGenerate(c.Request.Context(), uri.ID)
	if c.Request.Context().Err() != nil {
		return
	}
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationQueryFailure)
		return
	}
	c.Header("Cache-Control", "no-store")
	dtocode.SystemSuccess.Success(resp, c)
}

// ConversationTurns
// @Tags Chat History
// @Summary 按原始执行顺序读取完整轮次（聊天/Trace/工具详情）
// @Description 首屏最新 limit 轮，返回正序；用 next_before 向前加载并前插。轮内 blocks 按 sequence 正序；工具输入输出合并一行，start_order/end_order 可还原并行工具时间线。不返回用于模型恢复的私有 metadata。
// @Param id path string true "会话雪花 ID"
// @Param data query dtochat.TurnPageReq true "历史游标"
// @Success 200 {object} dtocode.Response{data=dtochat.TurnListResp}
// @Router /v1/chat/conversation/{id}/turns [get]
func (b *ChatApiV1Group) ConversationTurns(c *gin.Context) {
	var uri dtochat.ConversationIDReq
	var req dtochat.TurnPageReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err := c.ShouldBindQuery(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationTurns(c.Request.Context(), uri.ID, &req)
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationQueryFailure)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// ConversationUpdate
// @Tags Chat History
// @Summary 修改会话标题或收藏状态
// @Param id path string true "会话雪花 ID"
// @Param data body dtochat.ConversationUpdateReq true "至少提供 title 或 favorite"
// @Success 200 {object} dtocode.Response{data=dtochat.ConversationResp}
// @Router /v1/chat/conversation/{id} [put]
func (b *ChatApiV1Group) ConversationUpdate(c *gin.Context) {
	var uri dtochat.ConversationIDReq
	var req dtochat.ConversationUpdateReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationUpdate(c.Request.Context(), uri.ID, &req)
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationUpdateFailure)
		return
	}
	dtocode.SystemSuccess.Success(resp, c)
}

// ConversationDelete
// @Tags Chat History
// @Summary 软删除会话（删除后不可查询或续聊）
// @Param id path string true "会话雪花 ID"
// @Success 200 {object} dtocode.Response
// @Router /v1/chat/conversation/{id} [delete]
func (b *ChatApiV1Group) ConversationDelete(c *gin.Context) {
	var uri dtochat.ConversationIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err := agentvc.ConversationDelete(c.Request.Context(), uri.ID); err != nil {
		conversationFailure(c, err, dtocode.ConversationDeleteFailure)
		return
	}
	dtocode.SystemSuccess.Success(nil, c)
}
