package global

import (
	"github.com/lyonmu/ai-demo-go/internal/config"
	"go.uber.org/zap"
)

var (
	Cfg    config.Cli
	Logger *zap.Logger
)
