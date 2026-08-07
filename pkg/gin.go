package pkg

import (
	"fmt"

	"github.com/gin-contrib/cors"
	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/prometheus/client_golang/prometheus"
)

func NewGin(reg *prometheus.Registry, mode bool) (*gin.Engine, error) {

	if !mode {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(cors.Default())
	r.Use(gin.Recovery())
	if mode {
		r.Use(gin.Logger())
		pprof.Register(r)
	}
	if err := RegisterMetrics(r, reg, fmt.Sprintf("%s/metrics", global.Cfg.RouterPrefix)); err != nil {
		return nil, err
	}

	return r, nil
}
