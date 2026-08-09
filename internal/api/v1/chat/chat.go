package chat

import (
	"github.com/gin-gonic/gin"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/global"
)

// ChatSSE
// @Tags      Chat
// @Summary   ChatSSE
// @Description 简单SSE对话
// @Param     data  body      dtochat.ChatReq      true  "用户发起的对话"
// @Produce   json
// @Router    /v1/chat/sse [POST]
func (b *ChatApiV1Group) ChatSSE(c *gin.Context) {

	var req dtochat.ChatReq

	if err := c.ShouldBindJSON(&req); err != nil {
		global.Logger.Sugar().Errorf("Request parameter error : %+v", err)
		dtocode.RequestParameterError.Failure(c)
		return
	}

	c.JSON(200, gin.H{
		"msg":  "pong",
		"data": req.Messages,
		"code": 200,
	})
}
