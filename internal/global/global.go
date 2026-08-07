package global

import (
	pkgid "github.com/lyonmu/gopkg/id"
	"github.com/lyonmu/kaguya/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

var (
	Cfg     config.Cli
	Logger  *zap.Logger
	Id      pkgid.IDGenerator
	Metrics *prometheus.Registry
)
