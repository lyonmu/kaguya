package pkg

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

const maxRequestBytes int64 = 1024 * 1024

// This is browser isolation for a single-user service, not authentication.
// Host validation also prevents same-origin requests after DNS rebinding.
func requestSecurity(trustedHosts []string) gin.HandlerFunc {
	allowed := map[string]bool{"localhost": true}
	for _, host := range trustedHosts {
		allowed[strings.ToLower(host)] = true
	}
	return func(c *gin.Context) {
		r := c.Request
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		u, err := url.Parse("http://" + r.Host)
		if err != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" ||
			(!allowed[strings.ToLower(u.Hostname())] && net.ParseIP(u.Hostname()) == nil) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "untrusted request host"})
			return
		}
		// Applies to every request, including SSE subscriptions.
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "cross-origin request denied"})
			return
		}
		if origins, present := r.Header["Origin"]; present {
			if len(origins) != 1 || !sameRequestOrigin(origins[0], r.Host) {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "cross-origin request denied"})
				return
			}
		}
		if r.ContentLength > maxRequestBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body exceeds 1 MiB"})
			return
		}
		r.Body = http.MaxBytesReader(c.Writer, r.Body, maxRequestBytes)
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
func desktopRequestSecurity() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		if c.Request.ContentLength > maxRequestBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body exceeds 1 MiB"})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBytes)
		c.Next()
	}
}
