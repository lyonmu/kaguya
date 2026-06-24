package global

import (
	"github.com/lyonmu/kaguya/internal/config"
	"go.uber.org/zap"
)

var (
	Cfg    config.Cli
	Logger *zap.Logger
)
