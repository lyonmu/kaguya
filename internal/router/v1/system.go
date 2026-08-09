package v1

import (
	"github.com/gin-gonic/gin"
	apiv1 "github.com/lyonmu/kaguya/internal/api/v1"
)

type SystemRouter struct{}

func (r *ChatRouter) InitSystemRouter(group *gin.RouterGroup, apiGroup apiv1.ApiV1Group) {

	chatRouter := group.Group("v1/system")
	{
		chatRouter.GET("accesslog/page", apiGroup.SystemAccessLogPage)
	}

}
