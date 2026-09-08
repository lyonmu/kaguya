package v1

import (
	"github.com/gin-gonic/gin"
	apiv1 "github.com/lyonmu/kaguya/internal/api/v1"
)

type SystemRouter struct{}

func (r *SystemRouter) InitSystemRouter(group *gin.RouterGroup, apiGroup apiv1.ApiV1Group) {
	systemRouter := group.Group("v1/system")
	{
		systemRouter.GET("accesslog/page", apiGroup.SystemAccessLogPage)

		systemRouter.GET("provider/page", apiGroup.SystemProviderPage)
		systemRouter.GET("provider/label", apiGroup.SystemProviderLabels)
		systemRouter.GET("provider/:id", apiGroup.SystemProviderDetail)
		systemRouter.POST("provider", apiGroup.SystemProviderCreate)
		systemRouter.PUT("provider/:id", apiGroup.SystemProviderUpdate)
		systemRouter.DELETE("provider/:id", apiGroup.SystemProviderDelete)

		systemRouter.GET("model/page", apiGroup.SystemModelPage)
		systemRouter.GET("model/label", apiGroup.SystemModelLabels)
		systemRouter.GET("model/:id", apiGroup.SystemModelDetail)
		systemRouter.POST("model", apiGroup.SystemModelCreate)
		systemRouter.PUT("model/:id", apiGroup.SystemModelUpdate)
		systemRouter.DELETE("model/:id", apiGroup.SystemModelDelete)
	}
}
