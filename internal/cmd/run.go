package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	pkgid "github.com/lyonmu/gopkg/id"
	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	"github.com/lyonmu/kaguya/internal/db"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	"github.com/lyonmu/kaguya/internal/global"
	initialize "github.com/lyonmu/kaguya/internal/init"
	"github.com/lyonmu/kaguya/internal/router"
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
	"github.com/lyonmu/kaguya/pkg"
	"go.uber.org/zap"
)

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

func Run() {

	var zapLogger *zap.Logger
	var err error
	zapLogger, err = global.Cfg.LogInfo.NewLogger()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create logger: %v\n", err)
		os.Exit(1)
	}

	global.Logger = zapLogger
	defer global.Logger.Sync()

	global.Logger.Info("application is starting...")
	if err := initialize.SQLCipherKey(&global.Cfg.DB); err != nil {
		global.Logger.Error("initialize SQLCipher key failed", zap.Error(err))
		os.Exit(1)
	}

	// 创建 ID 生成器，传入机器 ID 获取函数
	gen, err := pkgid.NewSonySnowFlake(func() (int, error) {
		return global.Cfg.MachineID, nil // 每台机器应使用不同的 ID
	})
	if err != nil {
		global.Logger.Sugar().Errorf("failed to create ID generator ,err is %s", err)
		os.Exit(1)
	}
	global.Id = gen

	global.Logger.Info("start init database connection")
	if err := global.Cfg.DB.EnsureSQLiteDatabase(); err != nil {
		global.Logger.Sugar().Errorf("ensure sqlite database failed, err is %s", err)
		os.Exit(1)
	}
	entcli, dbErr := db.InitSQLite(&global.Cfg.DB)
	if dbErr != nil {
		global.Logger.Sugar().Errorf("init sqlite conn failed, err is %s", dbErr)
		os.Exit(1)
	}
	db.EntClient = entcli
	defer db.EntClient.Close()

	global.Logger.Info("start init application data")
	initCtx, cancelInit := context.WithTimeout(context.Background(), 30*time.Second)
	initErr := initialize.Run(initCtx, db.EntClient)
	if initErr == nil {
		// 上次进程崩溃/强杀会留下 running 占位轮次：本实例启动时不接管它们。
		initErr = serviceagent.ReconcileRunningTurns(initCtx)
	}
	cancelInit()
	if initErr != nil {
		global.Logger.Error("initialize application data failed", zap.Error(initErr))
		os.Exit(1)
	}

	// API Key 静态加密必须在读取提供商配置前就绪；初始化失败（旧格式未迁移、
	// 密钥材料不匹配）直接中止启动，避免用不可用密钥继续处理请求。
	dbKey, keyErr := global.Cfg.DB.SQLCipherKeyBytes()
	if keyErr != nil {
		global.Logger.Error("read SQLCipher key for provider secret encryption failed", zap.Error(keyErr))
		os.Exit(1)
	}
	secretCtx, cancelSecret := context.WithTimeout(context.Background(), 30*time.Second)
	secretErr := (&servicesystem.SystemSvc{}).InitSecret(secretCtx, global.Cfg.SecretKey, dbKey)
	cancelSecret()
	if secretErr != nil {
		global.Logger.Error("initialize provider API key encryption failed", zap.Error(secretErr))
		os.Exit(1)
	}
	// serviceCtx 随 SIGTERM/中断取消，传播到 HTTP 请求与 SSE 流。
	serviceCtx, cancelService := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancelService()
	restoreDone := make(chan struct{})
	defer func() { cancelService(); <-restoreDone; agentmcp.Default.Close() }()
	go func() {
		defer close(restoreDone)
		if err := (&servicesystem.SystemSvc{}).RestoreMCP(serviceCtx); err != nil && serviceCtx.Err() == nil {
			global.Logger.Error("restore MCP configuration failed")
		}
	}()

	global.Metrics = pkg.NewPrometheusRegistry()
	global.Logger.Info("start init register gin engine")
	ginEngine, err := pkg.NewGin(global.Cfg.Debug, global.Cfg.TrustedHosts...)
	if err != nil {
		global.Logger.Error("failed to create gin engine", zap.Error(err))
		os.Exit(1)
	}

	global.Logger.Info("start init register metrics")
	if err := pkg.RegisterMetrics(ginEngine, global.Metrics, fmt.Sprintf("%s/metrics", global.Cfg.RouterPrefix)); err != nil {
		global.Logger.Error("failed to register metrics endpoint", zap.Error(err))
		os.Exit(1)
	}

	router.InitRouter(ginEngine)

	address := global.Cfg.ListenAddress()
	global.Logger.Sugar().Infof("kaguya is listening on http://%s", address)
	server := &http.Server{
		Addr: address, Handler: ginEngine,
		BaseContext:       func(net.Listener) context.Context { return serviceCtx },
		ErrorLog:          zap.NewStdLog(global.Logger),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 * 1024,
		// Streaming chats must not inherit a short HTTP write timeout.
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ListenAndServe() }()
	select {
	case <-serviceCtx.Done():
		// 关停顺序：先停止接纳新任务并取消连接与后台任务，等待终态落库，
		// 最后关闭工作区资源和数据库，避免数据库关闭后仍有写入。
		cancelService()
		serviceagent.Shutdown()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownCtx); err != nil {
			global.Logger.Warn("graceful HTTP shutdown timed out", zap.Error(err))
			_ = server.Close()
		}
		// 最终落库预算覆盖 recorder 的单次 15 秒 flush 与终态标记。
		waitCtx, cancelWait := context.WithTimeout(context.Background(), 25*time.Second)
		if err := serviceagent.WaitActive(waitCtx); err != nil {
			global.Logger.Warn("timed out waiting for active chat turns",
				zap.Error(err), zap.Int64("pending", serviceagent.PendingWork()))
		}
		cancelWait()
	case err := <-serverDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			global.Logger.Error("HTTP server failed", zap.Error(err))
		}
	}
}
