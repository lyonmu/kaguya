package chat

import (
	"github.com/gin-gonic/gin"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
)

// ConversationContext
// @Tags Chat History
// @Summary 最近完整轮次的整个会话上下文占用
// @Description 以最后一次模型调用输入（含缓存）与输出估算上下文，分母为该轮模型窗口的90%；不累加历史计费用量，不裁剪消息。旧记录或供应商未报告用量时 percent 为 null。
// @Param id path string true "会话 ID"
// @Success 200 {object} dtocode.Response{data=dtochat.ConversationContextResp}
// @Router /v1/chat/conversation/{id}/context [get]
func (b *ChatApiV1Group) ConversationContext(c *gin.Context) {
	var uri dtochat.ConversationIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationContext(c.Request.Context(), uri.ID)
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationQueryFailure)
		return
	}
	c.Header("Cache-Control", "no-store")
	dtocode.SystemSuccess.Success(resp, c)
}
