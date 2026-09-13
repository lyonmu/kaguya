package pkg

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestSecurity(t *testing.T) {
	r, err := NewGin(false, "agent.example.com")
	if err != nil {
		t.Fatal(err)
	}
	r.Any("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	for _, tc := range []struct {
		name, host, origin, site, method string
		want                             int
	}{
		{"CLI", "127.0.0.1:9024", "", "", "POST", 204},
		{"browser", "localhost:9024", "http://localhost:9024", "same-origin", "POST", 204},
		{"dev proxy", "localhost:5173", "http://localhost:5173", "same-origin", "POST", 204},
		{"IPv6", "[::1]:9024", "http://[::1]:9024", "", "GET", 204},
		{"TLS proxy", "agent.example.com", "https://agent.example.com", "same-origin", "POST", 204},
		{"TLS proxy missing site header", "agent.example.com", "https://agent.example.com", "", "GET", 204},
		{"TLS proxy wrong origin", "agent.example.com", "https://other.example.com", "same-origin", "POST", 403},
		{"TLS proxy wrong host", "other.example.com", "https://agent.example.com", "same-origin", "POST", 403},
		{"foreign GET", "localhost:9024", "https://evil.example", "", "GET", 403},
		{"foreign POST", "localhost:9024", "https://evil.example", "", "POST", 403},
		{"foreign preflight", "localhost:9024", "https://evil.example", "", "OPTIONS", 403},
		{"opaque origin", "localhost:9024", "null", "", "POST", 403},
		{"different port", "localhost:9024", "http://localhost:9999", "", "POST", 403},
		{"same site", "localhost:9024", "", "same-site", "GET", 403},
		{"cross site image", "localhost:9024", "", "cross-site", "GET", 403},
		{"DNS rebind", "evil.example:9024", "http://evil.example:9024", "same-origin", "GET", 403},
		{"userinfo", "localhost:9024", "http://evil@localhost:9024", "", "POST", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://"+tc.host+"/", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			req.Header.Set("Sec-Fetch-Site", tc.site)
			req.Header.Set("X-Forwarded-Host", "localhost:9024")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("permissive CORS")
			}
		})
	}
}

func TestRequestBodyLimit(t *testing.T) {
	r, _ := NewGin(false)
	called := false
	r.POST("/", func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			c.Status(413)
			return
		}
		called = true
		c.Status(204)
	})
	for _, chunked := range []bool{false, true} {
		req := httptest.NewRequest("POST", "http://localhost/", strings.NewReader(strings.Repeat("x", int(maxRequestBytes)+1)))
		if chunked {
			req.ContentLength = -1
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 413 || called {
			t.Fatalf("chunked=%v status=%d called=%v", chunked, w.Code, called)
		}
	}
}

// TestDesktopGinSkipsWebOriginRules 确认 Desktop 入口不套用 Web 的 Host/Origin
// 白名单（原生通道来源在 Assets 层校验），但仍保留体积限制与安全头。
func TestDesktopGinSkipsWebOriginRules(t *testing.T) {
	r, err := NewDesktopGin(false)
	if err != nil {
		t.Fatal(err)
	}
	r.Any("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "http://localhost/", strings.NewReader("{}"))
	req.Header.Set("Origin", "null")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("desktop engine must keep the shared security headers")
	}

	oversized := httptest.NewRequest(http.MethodPost, "http://localhost/", strings.NewReader("{}"))
	oversized.ContentLength = maxRequestBytes + 1
	w = httptest.NewRecorder()
	r.ServeHTTP(w, oversized)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d", w.Code)
	}
}

// TestWebGinStillRejectsOpaqueOrigin 确认 Web 入口不放行 Desktop 的原生来源特例。
func TestWebGinStillRejectsOpaqueOrigin(t *testing.T) {
	r, err := NewGin(false)
	if err != nil {
		t.Fatal(err)
	}
	r.Any("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	for _, origin := range []string{"null", "wails://localhost"} {
		req := httptest.NewRequest(http.MethodPost, "http://localhost:9024/", nil)
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("origin %q status=%d", origin, w.Code)
		}
	}
}
