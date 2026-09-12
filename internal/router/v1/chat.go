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
		chatRouter.GET("ws", apiGroup.ChatWS)
		chatRouter.GET("conversation/page", apiGroup.ConversationPage)
		chatRouter.GET("conversation/:id", apiGroup.ConversationDetail)
		chatRouter.GET("conversation/:id/turns", apiGroup.ConversationTurns)
		chatRouter.GET("conversation/:id/turns/:turn/blocks/:sequence", apiGroup.ConversationBlock)
		chatRouter.GET("conversation/:id/context", apiGroup.ConversationContext)
		chatRouter.GET("conversation/:id/title/wait", apiGroup.ConversationTitleWait)
		chatRouter.POST("conversation/:id/title/wait", apiGroup.ConversationTitleGenerate)
		chatRouter.POST("conversation/:id/stop", apiGroup.ConversationStop)
		chatRouter.PUT("conversation/:id", apiGroup.ConversationUpdate)
		chatRouter.DELETE("conversation/:id", apiGroup.ConversationDelete)
	}

}
