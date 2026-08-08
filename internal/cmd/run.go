package cmd

import (
	"fmt"
	"os"

	pkgid "github.com/lyonmu/gopkg/id"
	"github.com/lyonmu/gopkg/logger"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/internal/router"
	"github.com/lyonmu/kaguya/pkg"
	"go.uber.org/zap"
)

// swag init -g core.go -o ./internal/docs --parseDependency --parseInternal

// @title                       kaguya Swagger API接口文档
// @description                 kaguya 后端
// @version                     v0.0.1
// @license.name                MIT
// @license.url                 https://github.com/lyonmu/kaguya/blob/master/LICENSE
// @contact.name                Lyon Mu
// @contact.url                 https://github.com/lyonmu
// @contact.email               lyonmu@foxmail.com
// @host                        http://localhost:9024
// @BasePath                    /kaguya/api
// @schemes                     http

func Run() {

	logger, err := logger.NewDefault()
	if err != nil {
		os.Exit(1)
	}

	global.Logger = logger
	defer global.Logger.Sync()

	// 创建 ID 生成器，传入机器 ID 获取函数
	gen, err := pkgid.NewSonySnowFlake(func() (int, error) {
		return global.Cfg.MachineID, nil // 每台机器应使用不同的 ID
	})
	if err != nil {
		global.Logger.Sugar().Errorf("failed to create ID generator ,err is %s", err)
		os.Exit(1)
	}
	global.Id = gen

	switch global.Cfg.DB.Kind {
	case consts.MySQL:
		if err := global.Cfg.DB.EnsureMySQLDatabase(); err != nil {
			global.Logger.Sugar().Errorf("ensure mysql database failederr is %s", err)
			os.Exit(1)
		}
		entcli, initErr := db.InitMySQL(&global.Cfg.DB, global.Cfg.Debug)
		if initErr != nil {
			global.Logger.Sugar().Errorf("init mysql conn failed ,err is %s", initErr)
			os.Exit(1)
		}
		db.EntClient = entcli
	case consts.SQLite:
		if err := global.Cfg.DB.EnsureSQLiteDatabase(); err != nil {
			global.Logger.Sugar().Errorf("ensure sqlite database failederr is %s", err)
			os.Exit(1)
		}
		entcli, initErr := db.InitSQLite(&global.Cfg.DB, global.Cfg.Debug)
		if initErr != nil {
			global.Logger.Sugar().Errorf("init sqlite conn failed ,err is %s", initErr)
			os.Exit(1)
		}
		db.EntClient = entcli

	}

	global.Metrics = pkg.NewPrometheusRegistry()

	ginEngine, err := pkg.NewGin(global.Metrics, global.Cfg.Debug)
	if err != nil {
		global.Logger.Error("failed to create gin engine", zap.Error(err))
		os.Exit(1)
	}
	router.InitRouter(ginEngine)

	global.Logger.Sugar().Infof("kaguya is running on port :%d", global.Cfg.Port)
	ginEngine.Run(fmt.Sprintf(":%d", global.Cfg.Port))
}
