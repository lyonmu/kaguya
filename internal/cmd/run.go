package cmd

import (
	"os"

	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/gopkg/logger"
)

func Run() {

	logger, err := logger.NewDefault()
	if err != nil {
		os.Exit(1)
	}
	global.Logger = logger
	defer global.Logger.Sync()

	global.Logger.Info("application started")
}
