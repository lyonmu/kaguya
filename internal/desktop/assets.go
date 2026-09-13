package desktop

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/lyonmu/kaguya/pkg"
)

// assetsHandler 实现 Wails 的用户 Handler：业务请求直接进入同进程 Gin，
// 只有尚未就绪、正在退出或原生窄接口才会在这里处理。
type assetsHandler struct {
	root      context.Context
	admission *pkg.Admission

	windowID     atomic.Uint64
	published    atomic.Pointer[http.Handler]
	clipboardSet clipboardWriter
	openExternal externalOpener
}

func newAssetsHandler(root context.Context, admission *pkg.Admission) *assetsHandler {
	return &assetsHandler{root: root, admission: admission}
}

// setWindow 记录受信任窗口 ID；请求必须携带匹配的框架注入头。
func (a *assetsHandler) setWindow(id uint) { a.windowID.Store(uint64(id)) }

// setClipboardWriter/setExternalOpener 在宿主 API 可用后注册系统操作。
func (a *assetsHandler) setClipboardWriter(writer clipboardWriter) { a.clipboardSet = writer }
func (a *assetsHandler) setExternalOpener(opener externalOpener)   { a.openExternal = opener }

// publish 发布装配完成的业务 Handler；发布后不再修改。
func (a *assetsHandler) publish(handler http.Handler) { a.published.Store(&handler) }

// ready 报告业务 Handler 是否已可用。
func (a *assetsHandler) ready() bool { return a.published.Load() != nil }

// nativeMiddleware 校验原生窗口通道的请求来源。它位于 Wails 保留路径之前，
// 让框架内部请求也经过同一入口校验。
func (a *assetsHandler) nativeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")

		if r.Host != "localhost" {
			rejectNative(w, "untrusted request host")
			return
		}
		if !a.trustedWindow(r) {
			rejectNative(w, "request does not belong to this window")
			return
		}
		if origins, present := r.Header["Origin"]; present {
			if len(origins) != 1 || !desktopOrigin(origins[0]) {
				rejectNative(w, "cross-origin request denied")
				return
			}
		}
		switch site := r.Header.Get("Sec-Fetch-Site"); site {
		case "", "none", "same-origin":
		default:
			rejectNative(w, "cross-origin request denied")
			return
		}
		if r.ContentLength > maxRequestBytes {
			http.Error(w, "request body exceeds 1 MiB", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		next.ServeHTTP(w, r)
	})
}

// trustedWindow 要求框架注入的窗口头精确等于本窗口。
func (a *assetsHandler) trustedWindow(r *http.Request) bool {
	id := a.windowID.Load()
	if id == 0 {
		return false
	}
	return r.Header.Get("x-wails-window-id") == strconv.FormatUint(id, 10)
}

// desktopOrigin 接受原生页面自身的 origin、不透明 origin 或未携带 Origin 的请求。
// 它不是身份凭据，只是原生通道内的额外约束。
func desktopOrigin(origin string) bool {
	return origin == "wails://localhost" || origin == "null"
}

// ServeHTTP 是 Assets 的用户 Handler：原生窄接口优先，其余请求交给已发布的
// 业务 Handler，并合并 root 取消。
func (a *assetsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == nativeRouteRoot || strings.HasPrefix(r.URL.Path, nativeRouteRoot+"/") {
		a.serveNative(w, r)
		return
	}

	handler := a.published.Load()
	if handler == nil {
		http.Error(w, "kaguya is starting", http.StatusServiceUnavailable)
		return
	}
	if !a.admission.Enter() {
		http.Error(w, "kaguya is shutting down", http.StatusServiceUnavailable)
		return
	}
	defer a.admission.Leave()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(a.root, cancel)
	defer stop()
	(*handler).ServeHTTP(w, r.WithContext(ctx))
}
