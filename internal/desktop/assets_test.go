package desktop

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lyonmu/kaguya/pkg"
)

// nativeRequest 构造通过原生通道进入的请求，默认携带受信任窗口与合法来源头。
func nativeRequest(t *testing.T, method, path string, body string, mutate func(*http.Request)) *http.Request {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "http://localhost"+path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("x-wails-window-id", "7")
	if mutate != nil {
		mutate(req)
	}
	return req
}

func TestNativeMiddlewareRejectsForeignRequests(t *testing.T) {
	newHandler := func() (*assetsHandler, *bool) {
		called := false
		return newAssetsHandler(context.Background(), pkg.NewAdmission()), &called
	}
	for _, tc := range []struct {
		name   string
		mutate func(*http.Request)
		want   int
	}{
		{"valid native page", nil, http.StatusNoContent},
		{"native origin", func(r *http.Request) { r.Header.Set("Origin", "wails://localhost") }, http.StatusNoContent},
		{"opaque origin", func(r *http.Request) { r.Header.Set("Origin", "null") }, http.StatusNoContent},
		{"fetch metadata none", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "none") }, http.StatusNoContent},
		{"fetch metadata same-origin", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") }, http.StatusNoContent},
		{"untrusted host", func(r *http.Request) { r.Host = "evil.example" }, http.StatusForbidden},
		{"missing window", func(r *http.Request) { r.Header.Del("x-wails-window-id") }, http.StatusForbidden},
		{"wrong window", func(r *http.Request) { r.Header.Set("x-wails-window-id", "8") }, http.StatusForbidden},
		{"http origin", func(r *http.Request) { r.Header.Set("Origin", "http://localhost") }, http.StatusForbidden},
		{"foreign origin", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, http.StatusForbidden},
		{"duplicate origin", func(r *http.Request) {
			r.Header.Add("Origin", "wails://localhost")
			r.Header.Add("Origin", "null")
		}, http.StatusForbidden},
		{"cross site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, http.StatusForbidden},
		{"same site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }, http.StatusForbidden},
		{"oversized body", func(r *http.Request) { r.ContentLength = maxRequestBytes + 1 }, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, called := newHandler()
			handler.setWindow(7)
			var next http.HandlerFunc = func(w http.ResponseWriter, r *http.Request) {
				*called = true
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.WriteHeader(http.StatusNoContent)
			}
			req := nativeRequest(t, http.MethodPost, "/kaguya/api/v1/system/info", "", tc.mutate)
			w := httptest.NewRecorder()
			handler.nativeMiddleware(next).ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if tc.want == http.StatusForbidden || tc.want == http.StatusRequestEntityTooLarge {
				if *called {
					t.Fatal("rejected request reached the business handler")
				}
				if w.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatal("security headers missing on rejection")
				}
			}
		})
	}
}

func TestAssetsServeHTTPServesPublishedHandler(t *testing.T) {
	handler := newAssetsHandler(context.Background(), pkg.NewAdmission())
	handler.setWindow(7)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, nativeRequest(t, http.MethodGet, "/kaguya/api/v1/system/info", "", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unpublished handler status=%d", w.Code)
	}

	published := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	handler.publish(published)
	if !handler.ready() {
		t.Fatal("handler not marked ready after publish")
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, nativeRequest(t, http.MethodGet, "/kaguya/api/v1/system/info", "", nil))
	if w.Code != http.StatusTeapot {
		t.Fatalf("published handler status=%d", w.Code)
	}
}

func TestAssetsServeHTTPCancelsWithRootContext(t *testing.T) {
	root, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()
	handler := newAssetsHandler(root, pkg.NewAdmission())
	handler.setWindow(7)

	seen := make(chan error, 1)
	handler.publish(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			seen <- r.Context().Err()
		case <-time.After(2 * time.Second):
			seen <- nil
		}
	}))
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), nativeRequest(t, http.MethodGet, "/kaguya/api/v1/chat/sse", "", nil))
	}()
	time.Sleep(20 * time.Millisecond)
	cancelRoot()
	select {
	case err := <-seen:
		if err == nil {
			t.Fatal("root cancellation did not reach the business request")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("business request did not finish")
	}
}

func TestAssetsServeHTTPRejectsAfterAdmissionStops(t *testing.T) {
	admission := pkg.NewAdmission()
	handler := newAssetsHandler(context.Background(), admission)
	handler.setWindow(7)
	published := false
	handler.publish(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { published = true }))

	admission.Stop()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, nativeRequest(t, http.MethodGet, "/kaguya/api/v1/system/info", "", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", w.Code)
	}
	if published {
		t.Fatal("request served after admission stopped")
	}
}
