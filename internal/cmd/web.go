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

	"github.com/lyonmu/kaguya/internal/global"
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
	"github.com/lyonmu/kaguya/pkg"
	"go.uber.org/zap"
)

// webShutdownTimeout 是 HTTP 优雅关闭预算；超时后强制关闭连接。
const webShutdownTimeout = 10 * time.Second

// webDrainBudget 是最终落库预算，覆盖 recorder 的单次 15 秒 flush 与终态标记。
const webDrainBudget = 25 * time.Second

// runWeb 启动原有 HTTP 服务：监听配置地址、注册原路由并等待信号。
func runWeb() error {
	serviceCtx, cancelService := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancelService()

	rt := newAppRuntime(serviceCtx)
	if err := rt.init(); err != nil {
		rt.close()
		return err
	}

	ginEngine, err := pkg.NewGin(global.Cfg.Debug, global.Cfg.TrustedHosts...)
	if err != nil {
		rt.close()
		return rt.fail(fmt.Errorf("create gin engine: %w", err))
	}
	handler, err := rt.newEngine(ginEngine)
	if err != nil {
		rt.close()
		return err
	}

	address := global.Cfg.ListenAddress()
	global.Logger.Sugar().Infof("kaguya is listening on http://%s", address)
	server := &http.Server{
		Addr: address, Handler: handler,
		BaseContext:       func(net.Listener) context.Context { return rt.ctx },
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
	case err := <-serverDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			global.Logger.Error("HTTP server failed", zap.Error(err))
		}
	}

	// 关停顺序：先停止接纳新任务并取消连接与后台任务，等待终态落库，
	// 最后关闭工作区资源和数据库，避免数据库关闭后仍有写入。
	rt.beginShutdown()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), webShutdownTimeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		global.Logger.Warn("graceful HTTP shutdown timed out", zap.Error(err))
		_ = server.Close()
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), webDrainBudget)
	defer cancelWait()
	_ = rt.gate.Wait(waitCtx)
	if err := serviceagent.WaitActive(waitCtx); err != nil {
		global.Logger.Warn("timed out waiting for active chat turns",
			zap.Error(err), zap.Int64("pending", serviceagent.PendingWork()))
	}
	rt.close()
	return nil
}
