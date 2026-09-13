package desktop

import (
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
)

const (
	// nativeRouteRoot 是本宿主自己的窄接口前缀，不会进入业务路由。
	nativeRouteRoot = "/__desktop"
	// maxRequestBytes 与 Web 模式保持一致的请求体上限。
	maxRequestBytes int64 = 1024 * 1024
	// maxClipboardBytes 限制写入系统剪贴板的文本长度。
	maxClipboardBytes = 512 * 1024
	// maxExternalURLBytes 限制交给系统浏览器的 URL 长度。
	maxExternalURLBytes = 8 * 1024
)

// 宿主支持的宿主系统操作；未注册时对应接口返回 503。
type clipboardWriter func(text string) bool
type externalOpener func(rawURL string) error

// serveNative 处理复制与打开外链两个窄接口；未知路径返回 404，不落入 SPA。
func (a *assetsHandler) serveNative(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !a.admission.Enter() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "kaguya is shutting down"})
		return
	}
	defer a.admission.Leave()

	switch r.URL.Path {
	case nativeRouteRoot + "/clipboard":
		a.serveClipboard(w, r)
	case nativeRouteRoot + "/open-external":
		a.serveOpenExternal(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown desktop endpoint"})
	}
}

func (a *assetsHandler) serveClipboard(w http.ResponseWriter, r *http.Request) {
	if a.clipboardSet == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "clipboard is unavailable"})
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := decodeNativeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(req.Text) > maxClipboardBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "clipboard text exceeds the limit"})
		return
	}
	if !a.clipboardSet(req.Text) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "clipboard write failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *assetsHandler) serveOpenExternal(w http.ResponseWriter, r *http.Request) {
	if a.openExternal == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "external links are unavailable"})
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := decodeNativeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(req.URL) > maxExternalURLBytes || !allowedExternalURL(req.URL) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "only absolute http and https URLs can be opened"})
		return
	}
	if err := a.openExternal(req.URL); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "opening the system browser failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// allowedExternalURL 只接受解析成功、无 userinfo 且 Host 非空的 http/https URL。
func allowedExternalURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.User != nil || parsed.Host == "" {
		return false
	}
	return true
}

// decodeNativeJSON 解码窄接口请求体，拒绝未知字段与多余内容。
func decodeNativeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid request body")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid request body")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func rejectNative(w http.ResponseWriter, message string) {
	http.Error(w, message, http.StatusForbidden)
}

// startupPage 是窗口在业务 Handler 发布前显示的受控页面，不加载任何外部资源。
func startupPage(title, detail string) string {
	return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>` + html.EscapeString(title) + `</title>
<style>
:root { color-scheme: dark light; }
body { margin: 0; min-height: 100vh; display: grid; place-items: center;
  font-family: -apple-system, BlinkMacSystemFont, "Helvetica Neue", sans-serif;
  background: #0b0d10; color: #e6e6e6; }
main { max-width: 32rem; padding: 2rem; text-align: center; }
h1 { font-size: 1.1rem; font-weight: 600; margin: 0 0 0.75rem; }
p { margin: 0; font-size: 0.85rem; line-height: 1.6; color: #9aa0a6; white-space: pre-wrap; word-break: break-word; }
</style>
</head>
<body><main><h1>` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(detail) + `</p></main></body>
</html>`
}

// quittingScript 在关闭流程中提示用户不要重复操作；由宿主通过 ExecJS 注入。
const quittingScript = `(function(){if(document.getElementById('kaguya-quitting'))return;` +
	`var e=document.createElement('div');e.id='kaguya-quitting';` +
	`e.textContent='Kaguya 正在退出，请稍候…';` +
	`e.style.cssText='position:fixed;inset:0;z-index:2147483647;display:flex;align-items:center;justify-content:center;` +
	`background:rgba(11,13,16,.82);color:#e6e6e6;font:14px -apple-system,BlinkMacSystemFont,sans-serif;';` +
	`document.body.appendChild(e);})();`
