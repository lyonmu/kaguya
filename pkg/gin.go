package pkg

import (
	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
)

func NewGin(mode bool, trustedHosts ...string) (*gin.Engine, error) {
	if !mode {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(requestSecurity(trustedHosts))
	r.Use(gin.Recovery())
	if mode {
		r.Use(gin.Logger())
		pprof.Register(r)
	}
	return r, nil
}
