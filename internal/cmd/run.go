package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	pkgid "github.com/lyonmu/gopkg/id"
	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
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
// @schemes                     https

func Run() {

	var zapLogger *zap.Logger
	var err error
	if global.Cfg.PrepareTLS {
		// stdout is a public PEM export; operational messages must go to stderr.
		zapLogger, err = zap.NewProduction()
	} else {
		zapLogger, err = global.Cfg.LogInfo.NewLogger()
	}
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
	switch global.Cfg.DB.Kind {
	// 遗留数据库启动分支暂时禁用，保留实现以便后续恢复。
	/*
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
		case consts.PostgreSQL, consts.Postgres:
			if err := global.Cfg.DB.EnsurePostgreSQLDatabase(); err != nil {
				global.Logger.Sugar().Errorf("ensure postgresql database failed, err is %s", err)
				os.Exit(1)
			}
			entcli, initErr := db.InitPostgreSQL(&global.Cfg.DB, global.Cfg.Debug)
			if initErr != nil {
				global.Logger.Sugar().Errorf("init postgresql conn failed, err is %s", initErr)
				os.Exit(1)
			}
			db.EntClient = entcli
	*/
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
	default:
		global.Logger.Sugar().Errorf("database kind %q is disabled; use sqlite", global.Cfg.DB.Kind)
		os.Exit(1)
	}
	defer db.EntClient.Close()

	global.Logger.Info("start init application data")
	initCtx, cancelInit := context.WithTimeout(context.Background(), 30*time.Second)
	initErr := initialize.Run(initCtx, db.EntClient)
	cancelInit()
	if initErr != nil {
		global.Logger.Error("initialize application data failed", zap.Error(initErr))
		os.Exit(1)
	}

	tlsCtx, cancelTLS := context.WithTimeout(context.Background(), 15*time.Second)
	if global.Cfg.RenewTLS {
		_, err := (&servicesystem.SystemSvc{}).TLSUpdate(tlsCtx, &dtosystem.TLSSaveReq{Generate: true, Hosts: append([]string{global.Cfg.Host}, global.Cfg.TrustedHosts...)})
		if err != nil {
			cancelTLS()
			global.Logger.Error("renew TLS configuration failed")
			os.Exit(1)
		}
	}
	tlsConfig, tlsErr := (&servicesystem.SystemSvc{}).PrepareTLS(tlsCtx, append([]string{global.Cfg.Host}, global.Cfg.TrustedHosts...))
	cancelTLS()
	if tlsErr != nil {
		global.Logger.Error("prepare TLS configuration failed", zap.Error(tlsErr))
		os.Exit(1)
	}
	// API Key 静态加密在读取提供商配置前就绪；失败时降级为明文存储并明确告警，
	// 以免密钥材料问题导致服务无法启动。
	secretCtx, cancelSecret := context.WithTimeout(context.Background(), 30*time.Second)
	secretErr := (&servicesystem.SystemSvc{}).InitSecret(secretCtx, global.Cfg.SecretKey)
	cancelSecret()
	if secretErr != nil {
		global.Logger.Warn("provider API key encryption is disabled; keys are stored as plaintext", zap.Error(secretErr))
	} else if !global.Cfg.SecretKeySet() {
		global.Logger.Warn("provider API keys use a key derived from the TLS certificate; set KAGUYA_SECRET_KEY to protect them independently of the database")
	}
	if global.Cfg.PrepareTLS {
		info, err := (&servicesystem.SystemSvc{}).Info(context.Background())
		if err != nil {
			global.Logger.Error("read public TLS certificate failed")
			os.Exit(1)
		}
		fmt.Print(info.TLS.CertificatePEM)
		return
	}
	mcpCtx, cancelMCP := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	restoreDone := make(chan struct{})
	defer func() { cancelMCP(); <-restoreDone; agentmcp.Default.Close() }()
	go func() {
		defer close(restoreDone)
		if err := (&servicesystem.SystemSvc{}).RestoreMCP(mcpCtx); err != nil && mcpCtx.Err() == nil {
			global.Logger.Error("restore MCP configuration failed")
		}
	}()

	global.Metrics = pkg.NewPrometheusRegistry()
	global.Logger.Info("start init register gin engine")
	ginEngine, err := pkg.NewGin(global.Metrics, global.Cfg.Debug, global.Cfg.TrustedHosts...)
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
	global.Logger.Sugar().Infof("kaguya is listening on https://%s (TLS 1.3 only)", address)
	server := &http.Server{
		Addr: address, Handler: ginEngine,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 * 1024,
		// Streaming chats must not inherit a short HTTP write timeout.
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ListenAndServeTLS("", "") }()
	select {
	case <-mcpCtx.Done():
		<-restoreDone
		agentmcp.Default.Close()
		serviceagent.Shutdown()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
		// 等待进行中的轮次落库/退出，避免数据库关闭后继续写入。
		waitCtx, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
		if err := serviceagent.WaitActive(waitCtx); err != nil {
			global.Logger.Warn("timed out waiting for active chat turns", zap.Error(err))
		}
		cancelWait()
	case err := <-serverDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			global.Logger.Error("HTTP server failed", zap.Error(err))
		}
	}
}
