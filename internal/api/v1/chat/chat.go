package chat

import (
	"github.com/gin-gonic/gin"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/pkg"
)

// Chat
// @Tags      对话
// @Summary   简单对话
// @Description 简单对话 chat
// @Param     data  body      dtochat.ChatReq      true  "用户发起的对话"
// @Produce   json
// @Router    /v1/chat [GET]
func (b *ChatApiV1Group) Chat(c *gin.Context) {

	var req dtochat.ChatReq

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(200, gin.H{
			"msg":  "pong",
			"data": err,
			"code": 500,
		})
		return
	}

	uaStr := c.Request.UserAgent()
	ua := pkg.ParseUserAgent(uaStr)
	access_ip := c.ClientIP()

	global.Logger.Sugar().Info(ua.Browser())

	global.Logger.Sugar().Info(access_ip)

	c.JSON(200, gin.H{
		"msg":  "pong",
		"data": req.Messages,
		"code": 200,
	})
}
