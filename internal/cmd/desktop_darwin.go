//go:build darwin

package cmd

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lyonmu/kaguya/internal/desktop"
	"github.com/lyonmu/kaguya/internal/global"
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
	"github.com/lyonmu/kaguya/pkg"
	"go.uber.org/zap"
)

// desktopDrainBudget 是排空在途请求与生成任务的单次等待预算；超时只告警并
// 继续等待，不能在任务仍使用数据库时提前释放连接。
const desktopDrainBudget = 25 * time.Second

// runDesktop 启动 macOS 原生窗口；业务请求经 Wails 原生通道直接进入同进程
// Gin，不创建任何入站监听端口。
func runDesktop() error {
	rt := newAppRuntime(context.Background())
	defer rt.close()

	host, err := desktop.New(desktop.Options{
		Name:        "Kaguya",
		Description: "Kaguya AI Agent Console",
		WindowTitle: "Kaguya Agent Console",
		Width:       1280,
		Height:      860,
		MinWidth:    960,
		MinHeight:   640,
		APIPrefix:   global.Cfg.RouterPrefix,
		RootContext: rt.RootContext(),
		Admission:   rt.Admission(),
		Start: func() (http.Handler, error) {
			if err := rt.init(); err != nil {
				return nil, err
			}
			ginEngine, err := pkg.NewDesktopGin(global.Cfg.Debug)
			if err != nil {
				return nil, err
			}
			return rt.newEngine(ginEngine)
		},
		Shutdown: func() { shutdownDesktop(rt) },
	})
	if err != nil {
		return err
	}

	// 原生信号与菜单退出走同一关闭状态机；Wails 默认信号处理已关闭。
	signals, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	go func() {
		<-signals.Done()
		host.RequestQuit()
	}()

	return host.Run()
}

// shutdownDesktop 先停止准入并取消连接，再等待 Handler 与生成任务排空，
// 最后释放 MCP、数据库与日志。
func shutdownDesktop(rt *appRuntime) {
	rt.beginShutdown()
	for {
		waitCtx, cancel := context.WithTimeout(context.Background(), desktopDrainBudget)
		handlersErr := rt.gate.Wait(waitCtx)
		var workErr error
		if handlersErr == nil {
			workErr = serviceagent.WaitActive(waitCtx)
		}
		cancel()
		if handlersErr == nil && workErr == nil {
			break
		}
		if global.Logger != nil {
			global.Logger.Warn("still draining work before desktop shutdown",
				zap.Int64("pending", serviceagent.PendingWork()))
		}
	}
	rt.close()
}
