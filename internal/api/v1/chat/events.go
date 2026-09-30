package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
)

// sseHeartbeatInterval 是长连接空闲时的注释帧间隔：保持代理与客户端连接，
// 不携带业务语义；阅读方本身仍以事件流为准。
const sseHeartbeatInterval = 15 * time.Second

// ChatEvents
// @Tags Chat
// @Summary 标题事件订阅（SSE）
// @Description 只推送已提交的标题更新与任务状态事件，形如 {"type":"conversation.title.updated",...}。数据库是权威来源：断线重连后客户端应重新读取会话列表并补齐快照，不提供事件重放。每 15 秒发送 comment 心跳。
// @Produce json
// @Success 200 {object} dtocode.Response{code=number,data=dtochat.TitleEvent,message=string} "SSE 流式响应"
// @Router /v1/events [get]
func (b *ChatApiV1Group) ChatEvents(c *gin.Context) {
	subscription, unsubscribe, err := serviceagent.DefaultTitlePublisher.Subscribe()
	if err != nil {
		dtocode.ServiceStopping.Failure(c)
		return
	}
	defer unsubscribe()

	c.Writer.Header().Set("Content-Type", "text/event-stream;charset=UTF-8")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush()

	heartbeat := time.NewTicker(sseHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case event, ok := <-subscription.Events:
			if !ok {
				// 慢订阅者被断开：客户端按断线重连逻辑重新读取快照。
				return
			}
			payload, err := json.Marshal(dtocode.Response{
				Code:    dtocode.SystemSuccess.Code,
				Data:    event,
				Message: dtocode.SystemSuccess.Message,
			})
			if err != nil {
				return
			}
			if !refreshSSEWriteDeadline(c) {
				return
			}
			if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", payload); err != nil {
				return
			}
			c.Writer.Flush()
		case <-heartbeat.C:
			if !refreshSSEWriteDeadline(c) {
				return
			}
			if _, err := c.Writer.WriteString(": heartbeat\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}
