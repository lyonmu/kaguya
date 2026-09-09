package chat

import (
	"github.com/gin-gonic/gin"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
)

// ConversationContext
// @Tags Chat History
// @Summary 整个会话累计 token 占比
// @Description 累计所有已完成轮次的 total_tokens（含跨模型和工具 step），分母为最新完整轮次模型窗口的90%；不代表实际上下文占用，不裁剪消息。无累计用量或模型窗口时 percent 为 null。
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
