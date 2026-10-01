package pkg

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
)

const maxRequestBytes int64 = 1024 * 1024

// This is browser isolation for a single-user service, not authentication.
// Host validation also prevents same-origin requests after DNS rebinding.
func requestSecurity(apiPrefix string, trustedHosts []string) gin.HandlerFunc {
	allowed := map[string]bool{"localhost": true}
	for _, host := range trustedHosts {
		allowed[strings.ToLower(host)] = true
	}
	// 全局指令路径列表可能很大：仅该端点解除固定 1 MiB 请求体上限，
	// 其它接口的 body 限制保持不变。
	bodyExemptPath := strings.TrimSuffix(apiPrefix, "/") + "/v1/system/info/agents-paths"
	return func(c *gin.Context) {
		r := c.Request
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		u, err := url.Parse("http://" + r.Host)
		if err != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" ||
			(!allowed[strings.ToLower(u.Hostname())] && net.ParseIP(u.Hostname()) == nil) {
			dtocode.WriteHTTP(c.Writer, http.StatusForbidden, dtocode.RequestForbidden)
			c.Abort()
			return
		}
		// Applies to every request, including SSE subscriptions.
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
			dtocode.WriteHTTP(c.Writer, http.StatusForbidden, dtocode.RequestForbidden)
			c.Abort()
			return
		}
		if origins, present := r.Header["Origin"]; present {
			if len(origins) != 1 || !sameRequestOrigin(origins[0], r.Host) {
				dtocode.WriteHTTP(c.Writer, http.StatusForbidden, dtocode.RequestForbidden)
				c.Abort()
				return
			}
		}
		if r.URL.Path != bodyExemptPath {
			if r.ContentLength > maxRequestBytes {
				dtocode.WriteHTTP(c.Writer, http.StatusRequestEntityTooLarge, dtocode.RequestTooLarge)
				c.Abort()
				return
			}
			r.Body = http.MaxBytesReader(c.Writer, r.Body, maxRequestBytes)
		}
		c.Next()
	}
}

func sameRequestOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	// The external scheme may be HTTPS behind a trusted TLS reverse proxy.
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") &&
		u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" &&
		strings.EqualFold(u.Host, host)
}

// desktopRequestSecurity 只保留两种模式共用的安全头与请求体限制。原生窗口
// 来源（Host、窗口 ID、Origin、Sec-Fetch-Site）由 Desktop 的 Assets 中间件
// 校验，这里不重复也不放宽 Web 的白名单。
func desktopRequestSecurity(apiPrefix string) gin.HandlerFunc {
	bodyExemptPath := strings.TrimSuffix(apiPrefix, "/") + "/v1/system/info/agents-paths"
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		if c.Request.URL.Path != bodyExemptPath {
			if c.Request.ContentLength > maxRequestBytes {
				dtocode.WriteHTTP(c.Writer, http.StatusRequestEntityTooLarge, dtocode.RequestTooLarge)
				c.Abort()
				return
			}
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBytes)
		}
		c.Next()
	}
}
