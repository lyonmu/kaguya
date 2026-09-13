//go:build darwin

package desktop

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// Host 是 macOS Desktop 宿主：负责原生窗口、资源通道与退出协调，
// 不注册业务 Binding，也不打开任何本地监听端口。
type Host struct {
	opts   Options
	assets *assetsHandler

	app    *application.App
	window *application.WebviewWindow

	clipboardMu sync.Mutex

	startOnce     sync.Once
	startDone     chan struct{}
	startDoneOnce sync.Once

	shutdownOnce    sync.Once
	shutdownStarted atomic.Bool
	quitReady       atomic.Bool
}

// New 创建宿主；只校验参数，此时还没有原生窗口。
func New(opts Options) (*Host, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	return &Host{
		opts:      opts,
		assets:    newAssetsHandler(opts.RootContext, opts.Admission),
		startDone: make(chan struct{}),
	}, nil
}

// Run 在调用方 goroutine 上运行原生事件循环，直到应用退出。
func (h *Host) Run() error {
	defer h.markStartDone()

	app := application.New(application.Options{
		Name:        h.opts.Name,
		Description: h.opts.Description,
		Assets: application.AssetOptions{
			Handler:        h.assets,
			Middleware:     h.assets.nativeMiddleware,
			DisableLogging: true,
		},
		// 退出由本宿主统一协调，信号也交给应用编排处理。
		ShouldQuit:                  h.shouldQuit,
		OnShutdown:                  h.onNativeShutdown,
		DisableDefaultSignalHandler: true,
		Mac: application.MacOptions{
			// 用户关闭最后一个窗口即申请退出；是否立即退出仍由 ShouldQuit 决定。
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})
	h.app = app

	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "kaguya",
		Title:     h.opts.WindowTitle,
		Width:     h.opts.Width,
		Height:    h.opts.Height,
		MinWidth:  h.opts.MinWidth,
		MinHeight: h.opts.MinHeight,
		BackgroundColour: application.RGBA{
			Red: 11, Green: 13, Blue: 16, Alpha: 255,
		},
		HTML: startupPage("Kaguya 正在启动", "正在初始化数据库与模型配置…"),
	})
	h.window = window
	h.assets.setWindow(window.ID())
	h.assets.setClipboardWriter(h.writeClipboard)
	h.assets.setExternalOpener(h.openExternal)

	window.RegisterHook(events.Common.WindowClosing, h.handleWindowClosing)
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go h.start()
	})

	return app.Run()
}

// ShowStartupError 用受控错误页替换窗口内容；错误信息只做转义展示。
func (h *Host) ShowStartupError(err error) {
	if h.window == nil {
		return
	}
	h.window.SetHTML(startupPage("Kaguya 启动失败", err.Error()))
}

// start 只运行一次业务初始化，成功后发布 Handler 并加载嵌入前端。
func (h *Host) start() {
	defer h.markStartDone()
	if h.shutdownStarted.Load() {
		return
	}
	h.startOnce.Do(func() {
		handler, err := h.opts.Start()
		if err != nil {
			h.ShowStartupError(err)
			return
		}
		// 退出已经开始时不再发布，避免把已关闭数据库的 Handler 暴露给窗口。
		if h.shutdownStarted.Load() {
			return
		}
		h.assets.publish(handler)
		h.window.SetURL("wails://localhost/#" + apiPrefixFragment(h.opts.APIPrefix))
	})
}

// writeClipboard 串行化 ClipboardManager 的惰性初始化与写入。
func (h *Host) writeClipboard(text string) bool {
	h.clipboardMu.Lock()
	defer h.clipboardMu.Unlock()
	if h.app == nil {
		return false
	}
	return h.app.Clipboard.SetText(text)
}

// openExternal 把已校验的 http/https 链接交给系统浏览器。
func (h *Host) openExternal(rawURL string) error {
	if h.app == nil {
		return errors.New("desktop host is not ready")
	}
	return h.app.Browser.OpenURL(rawURL)
}

func (h *Host) markStartDone() {
	h.startDoneOnce.Do(func() { close(h.startDone) })
}
