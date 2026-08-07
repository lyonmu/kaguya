package router

import (
	"github.com/gin-gonic/gin"
	_ "github.com/lyonmu/kaguya/docs"
	"github.com/lyonmu/kaguya/internal/global"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

type RouterGroup struct {
}

func InitRouter(e *gin.Engine) {

	// Router group
	group := e.Group(global.Cfg.RouterPrefix)

	// swagger
	swaggerRouter := group.Group("swagger")
	{
		swaggerRouter.GET("/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	}

	global.Logger.Sugar().Info("router http register success")

}
