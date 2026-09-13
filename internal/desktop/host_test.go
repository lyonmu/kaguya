package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lyonmu/kaguya/pkg"
)

func TestNativeEndpoints(t *testing.T) {
	var copied string
	var opened string
	handler := newAssetsHandler(context.Background(), pkg.NewAdmission())
	handler.setWindow(7)
	handler.setClipboardWriter(func(text string) bool { copied = text; return true })
	handler.setExternalOpener(func(rawURL string) error { opened = rawURL; return nil })

	post := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, nativeRequest(t, http.MethodPost, path, body, nil))
		return w
	}

	if w := post("/__desktop/clipboard", `{"text":"你好"}`); w.Code != http.StatusOK {
		t.Fatalf("clipboard status=%d body=%s", w.Code, w.Body)
	}
	if copied != "你好" {
		t.Fatalf("clipboard text=%q", copied)
	}
	if w := post("/__desktop/open-external", `{"url":"https://example.com/a?b=1"}`); w.Code != http.StatusOK {
		t.Fatalf("open-external status=%d body=%s", w.Code, w.Body)
	}
	if opened != "https://example.com/a?b=1" {
		t.Fatalf("opened url=%q", opened)
	}

	for _, tc := range []struct {
		name, path, body string
		want             int
	}{
		{"unknown native path", "/__desktop/exec", `{}`, http.StatusNotFound},
		{"unknown field", "/__desktop/clipboard", `{"text":"a","command":"rm"}`, http.StatusBadRequest},
		{"trailing json", "/__desktop/clipboard", `{"text":"a"}{"text":"b"}`, http.StatusBadRequest},
		{"malformed json", "/__desktop/clipboard", `{`, http.StatusBadRequest},
		{"oversized text", "/__desktop/clipboard", `{"text":"` + strings.Repeat("a", maxClipboardBytes+1) + `"}`, http.StatusRequestEntityTooLarge},
		{"file url", "/__desktop/open-external", `{"url":"file:///etc/passwd"}`, http.StatusBadRequest},
		{"javascript url", "/__desktop/open-external", `{"url":"javascript:alert(1)"}`, http.StatusBadRequest},
		{"data url", "/__desktop/open-external", `{"url":"data:text/html,x"}`, http.StatusBadRequest},
		{"userinfo", "/__desktop/open-external", `{"url":"https://evil@example.com/"}`, http.StatusBadRequest},
		{"missing host", "/__desktop/open-external", `{"url":"https:///path"}`, http.StatusBadRequest},
		{"oversized url", "/__desktop/open-external", `{"url":"https://example.com/` + strings.Repeat("a", maxExternalURLBytes) + `"}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := post(tc.path, tc.body); w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}

	// 非 POST 方法不执行业务动作。
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, nativeRequest(t, http.MethodGet, "/__desktop/clipboard", "", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method not allowed status=%d", w.Code)
	}
	// 未注册系统能力时拒绝而不是虚报成功。
	empty := newAssetsHandler(context.Background(), pkg.NewAdmission())
	empty.setWindow(7)
	w = httptest.NewRecorder()
	empty.ServeHTTP(w, nativeRequest(t, http.MethodPost, "/__desktop/clipboard", `{"text":"a"}`, nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unregistered clipboard status=%d", w.Code)
	}
}

func TestNativeEndpointResponsesAreNotCached(t *testing.T) {
	handler := newAssetsHandler(context.Background(), pkg.NewAdmission())
	handler.setWindow(7)
	handler.setClipboardWriter(func(string) bool { return true })
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, nativeRequest(t, http.MethodPost, "/__desktop/clipboard", `{"text":"a"}`, nil))
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control=%q", w.Header().Get("Cache-Control"))
	}
	var payload map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || !payload["ok"] {
		t.Fatalf("body=%s", w.Body)
	}
}

func TestStartupPageEscapesErrorDetail(t *testing.T) {
	page := startupPage("启动失败", `<script>alert(1)</script>`)
	if strings.Contains(page, "<script>") {
		t.Fatal("startup page did not escape the error detail")
	}
}
