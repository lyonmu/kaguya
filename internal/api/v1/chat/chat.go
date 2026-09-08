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
// @Description SSE 对话：chat.id 为会话雪花 ID，后续请求沿用该 ID；chat.flag=start/delta/done/error。delta 携带 block（text/reasoning/tool_call/tool_result），phase=start/delta/block_end。正文与思考的 block_end 不重复内容；唯一的整轮 done 仅携带 Token 用量
// @Param     data  body      dtochat.ChatReq      true  "用户发起的对话（conversation_id 为空时开启新对话）"
// @Produce   json
// @Success   200  {object}  dtocode.Response{code=number,data=dtochat.ChatResp,message=string}  "SSE 流式响应，每帧为一个 ChatResp"
// @Router    /v1/chat/sse [POST]
func (b *ChatApiV1Group) ChatSSE(c *gin.Context) {

	var req dtochat.ChatReq

	if err := c.ShouldBindJSON(&req); err != nil {
		global.Logger.Sugar().Warnf("Request parameter error: %v", err)
		dtocode.RequestParameterError.Failure(c)
		return
	}

	fail, err := json.Marshal(dtocode.ChatSSEFailure)
	if err != nil {
		global.Logger.Sugar().Errorf("marshal sse failure response failed : %+v", err)
		dtocode.ChatSSEFailure.Failure(c)
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream;charset=UTF-8")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush()

	dataChan := make(chan *dtochat.ChatResp)
	go agentvc.Chat(c.Request.Context(), dataChan, &req)

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case v, ok := <-dataChan:
			if !ok {
				c.Writer.Flush()
				return
			}

			// 与 WS 下行帧共用同一套 flag 映射（start/delta/done/error）
			re, isErr := mapFrame(v)

			data, err := json.Marshal(re)
			if err != nil {
				global.Logger.Sugar().Errorf("marshal sse response failed : %+v", err)
				fmt.Fprintf(c.Writer, "data: %s\n\n", fail)
				c.Writer.Flush()
				return
			}

			if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", data); err != nil {
				return
			}
			c.Writer.Flush()

			if isErr {
				return
			}
		}
	}
}
