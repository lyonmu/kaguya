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

	c.Writer.WriteHeader(200)
	c.Writer.Header().Set("X-Content-Type-Options", "nosniff")
	c.Writer.Header().Set("Content-Type", "")
	c.Writer.WriteHeaderNow()
	dataChan := make(chan *dtochat.ChatSSEResp)
	// 开启协程实现 SSE 推送
	go agentvc.ChatSSE(c.Request.Context(), dataChan, &req)
	for range dataChan {
		c.Writer.Write([]byte{}) // 产生数据
		c.Writer.Flush()         // 产生一定的数据后， flush到浏览器端
	}
	c.Writer.Flush() // 最后 flush 一次

	c.Next()
	return
}
