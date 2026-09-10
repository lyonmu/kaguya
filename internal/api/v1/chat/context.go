package chat

import (
	"github.com/gin-gonic/gin"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
)

// ConversationContext
// @Tags Chat History
// @Summary 当前上下文占用
// @Description 最近一次模型调用的输入（含缓存）及输出估算上下文占用，分母为模型窗口的90%；达到阈值后下一次模型调用前自动压缩。无供应商用量或模型窗口时 percent 为 null。
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
