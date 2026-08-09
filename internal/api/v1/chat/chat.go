package chat

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/global"
)

// ChatSSE
// @Tags      Chat
// @Summary   ChatSSE
// @Description 简单SSE对话：流式返回模型回答，首帧携带会话ID与模型信息，末帧携带完整内容与token用量
// @Param     data  body      dtochat.ChatReq      true  "用户发起的对话（conversation_id 为空时开启新对话）"
// @Produce   json
// @Success   200  {object}  dtochat.ChatSSEResp  "SSE 流式响应，每帧为一个 ChatSSEResp"
// @Router    /v1/chat/sse [POST]
func (b *ChatApiV1Group) ChatSSE(c *gin.Context) {

	var req dtochat.ChatReq

	if err := c.ShouldBindJSON(&req); err != nil {
		global.Logger.Sugar().Errorf("Request parameter error : %+v", err)
		dtocode.RequestParameterError.Failure(c)
		return
	}

	// SSE 响应头必须在 WriteHeader 之前设置
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush()

	dataChan := make(chan *dtochat.ChatSSEResp)
	go agentvc.ChatSSE(c.Request.Context(), dataChan, &req)

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case v, ok := <-dataChan:
			if !ok {
				c.Writer.Flush()
				return
			}
			data, err := json.Marshal(v)
			if err != nil {
				global.Logger.Sugar().Errorf("marshal sse response failed : %+v", err)
				return
			}
			if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", data); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}
