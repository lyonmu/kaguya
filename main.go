package main

import (
	"fmt"
	"os"

	"github.com/alecthomas/kong"
	"github.com/lyonmu/kaguya/internal/cmd"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/gopkg/version"
)

func main() {
	kong.Parse(&global.Cfg,
		kong.Name("kaguya"),
		kong.Description("A simple AI demo application"),
		kong.UsageOnError(),
		kong.HelpOptions{
			Compact: true,
			Summary: true,
		},
	)
	if global.Cfg.Version {
		fmt.Println(version.Print("kaguya"))
		os.Exit(0)
	}

	cmd.Run()
}
