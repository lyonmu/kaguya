package pkg

import (
	"log"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
)

// NewGin 构造 Web 模式引擎：保留原有 Host/Origin/Sec-Fetch-Site 校验。
// apiPrefix 是业务 API 前缀，仅用于把 panic 转成统一 envelope。
func NewGin(mode bool, apiPrefix string, trustedHosts ...string) (*gin.Engine, error) {
	r := newEngine(mode, apiPrefix)
	r.Use(requestSecurity(apiPrefix, trustedHosts))
	return r, nil
}

// NewDesktopGin 构造 Desktop 原生通道使用的引擎。请求来源校验由 Assets 外层
// 中间件完成，这里只复用通用安全头、请求体限制与 Recovery/日志装配。
func NewDesktopGin(mode bool, apiPrefix string) (*gin.Engine, error) {
	r := newEngine(mode, apiPrefix)
	r.Use(desktopRequestSecurity(apiPrefix))
	return r, nil
}

func newEngine(mode bool, apiPrefix string) *gin.Engine {
	if !mode {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(apiRecovery(apiPrefix))
	if mode {
		r.Use(gin.Logger())
		pprof.Register(r)
	}
	return r
}

// apiRecovery 把 panic 转成统一 envelope。非 API 路径（静态资源、pprof）保持
// 裸 500，避免对框架自身响应强加业务协议。
func apiRecovery(apiPrefix string) gin.HandlerFunc {
	prefix := "/" + strings.Trim(apiPrefix, "/")
	return gin.CustomRecoveryWithWriter(gin.DefaultErrorWriter, func(c *gin.Context, recovered any) {
		if prefix != "/" && (c.Request.URL.Path == prefix || strings.HasPrefix(c.Request.URL.Path, prefix+"/")) {
			dtocode.WriteHTTP(c.Writer, http.StatusInternalServerError, dtocode.InternalFailure)
		} else {
			c.Status(http.StatusInternalServerError)
		}
		log.Printf("panic recovered: %v\n%s", recovered, debug.Stack())
		c.Abort()
	})
}
