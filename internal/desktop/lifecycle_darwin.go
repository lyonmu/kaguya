//go:build darwin

package desktop

import "github.com/wailsapp/wails/v3/pkg/application"

// 退出协调：Cmd+Q、菜单、窗口关闭与信号先进入同一个关闭状态机，
// 排空完成前 ShouldQuit 拒绝退出，避免事件循环在数据库仍在写入时结束。

// RequestQuit 发起一次统一关闭流程；重复调用不会重复执行。
func (h *Host) RequestQuit() {
	h.shutdownOnce.Do(func() {
		h.shutdownStarted.Store(true)
		go h.shutdownWorker()
	})
}

// shutdownWorker 按顺序排空：等初始化结束，执行应用释放，再真正退出事件循环。
func (h *Host) shutdownWorker() {
	h.showQuitting()
	<-h.startDone
	if h.opts.Shutdown != nil {
		h.opts.Shutdown()
	}
	h.quitReady.Store(true)
	if h.app != nil {
		h.app.Quit()
	}
}

// shouldQuit 由 AppKit 在主线程调用，必须立即返回；首次请求返回 false 并
// 异步开始关闭，排空完成后再返回 true 放行真正的退出。
func (h *Host) shouldQuit() bool {
	if h.quitReady.Load() {
		return true
	}
	h.RequestQuit()
	return false
}

// handleWindowClosing 拦截用户关闭窗口：先进入关闭流程并提示状态，
// 避免事件循环在数据库排空完成前结束。
func (h *Host) handleWindowClosing(event *application.WindowEvent) {
	if h.quitReady.Load() {
		return
	}
	h.RequestQuit()
	h.showQuitting()
	event.Cancel()
}

// showQuitting 在页面顶层显示退出提示；只使用内置受控脚本。
func (h *Host) showQuitting() {
	if h.window == nil {
		return
	}
	h.window.ExecJS(quittingScript)
}

// onNativeShutdown 是框架同步收尾钩子，只做轻量记录：真正的资源释放已经在
// shutdownWorker 中完成，不能在这里阻塞主线程等待原生请求。
func (h *Host) onNativeShutdown() {
	if !h.quitReady.Load() {
		h.RequestQuit()
	}
}
