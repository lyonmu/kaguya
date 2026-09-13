package pkg

import (
	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
)

// NewGin 构造 Web 模式引擎：保留原有 Host/Origin/Sec-Fetch-Site 校验。
func NewGin(mode bool, trustedHosts ...string) (*gin.Engine, error) {
	r := newEngine(mode)
	r.Use(requestSecurity(trustedHosts))
	return r, nil
}

// NewDesktopGin 构造 Desktop 原生通道使用的引擎。请求来源校验由 Assets 外层
// 中间件完成，这里只复用通用安全头、请求体限制与 Recovery/日志装配。
func NewDesktopGin(mode bool) (*gin.Engine, error) {
	r := newEngine(mode)
	r.Use(desktopRequestSecurity())
	return r, nil
}

func newEngine(mode bool) *gin.Engine {
	if !mode {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.Recovery())
	if mode {
		r.Use(gin.Logger())
		pprof.Register(r)
	}
	return r
}
