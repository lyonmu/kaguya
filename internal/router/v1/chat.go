package v1

import (
	"github.com/gin-gonic/gin"
	apiv1 "github.com/lyonmu/kaguya/internal/api/v1"
)

type ChatRouter struct{}

func (r *ChatRouter) InitChatRouter(group *gin.RouterGroup, apiGroup apiv1.ApiV1Group) {

	chatRouter := group.Group("v1/chat")
	{
		chatRouter.POST("sse", apiGroup.ChatSSE)
	}

}
