package router

import (
	"github.com/gin-gonic/gin"
	_ "github.com/lyonmu/kaguya/docs"
	apiv1 "github.com/lyonmu/kaguya/internal/api/v1"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/internal/middleware"
	routerv1 "github.com/lyonmu/kaguya/internal/router/v1"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

var (
	v1route = routerv1.V1Router{}
	v1api   = apiv1.ApiV1Group{}
)

type RouterGroup struct {
}

func InitRouter(e *gin.Engine) {

	// 创建操作日志中间件
	accessLogMiddleware := middleware.NewAccessLogMiddleware()

	// Router group
	group := e.Group(global.Cfg.RouterPrefix)

	group.Use(accessLogMiddleware.AccessLog())

	// swagger
	swaggerRouter := group.Group("swagger")
	{
		swaggerRouter.GET("/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	}

	v1route.InitChatRouter(group, v1api)
	v1route.InitSystemRouter(group, v1api)

	global.Logger.Sugar().Info("router http register success")

}
