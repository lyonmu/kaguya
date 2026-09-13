package cmd

import "github.com/lyonmu/kaguya/internal/global"

// go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g ./internal/cmd/run.go -o ./docs --parseDependency --parseInternal

// @title                       kaguya Swagger API接口文档
// @description                 kaguya 后端
// @version                     v0.0.1
// @license.name                MIT
// @license.url                 https://github.com/lyonmu/kaguya/blob/master/LICENSE
// @contact.name                Lyon Mu
// @contact.url                 https://github.com/lyonmu
// @contact.email               lyonmu@foxmail.com
// @host                        localhost:9024
// @BasePath                    /kaguya/api
// @schemes                     http

// Run 按启动模式分流：默认在 macOS 打开 Desktop 窗口，显式 --web 才运行
// 原来的 HTTP 服务。两种模式共享同一套初始化与释放流程。
func Run() error {
	if global.Cfg.Web {
		return runWeb()
	}
	return runDesktop()
}
