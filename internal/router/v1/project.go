package v1

import (
	"github.com/gin-gonic/gin"
	apiv1 "github.com/lyonmu/kaguya/internal/api/v1"
)

type ProjectRouter struct{}

func (*ProjectRouter) InitProjectRouter(group *gin.RouterGroup, apiGroup apiv1.ApiV1Group) {
	r := group.Group("v1/project")
	r.GET("page", apiGroup.ProjectPage)
	r.GET("directories", apiGroup.ProjectDirectories)
	r.GET(":id/files", apiGroup.ProjectFiles)
	r.GET(":id", apiGroup.ProjectDetail)
	r.POST("", apiGroup.ProjectCreate)
	r.PUT(":id", apiGroup.ProjectUpdate)
	r.DELETE(":id", apiGroup.ProjectDelete)
}
