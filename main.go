package main

import (
	"fmt"
	"os"

	"github.com/alecthomas/kong"
	"github.com/lyonmu/ai-demo-go/internal/cmd"
	"github.com/lyonmu/ai-demo-go/internal/global"
	"github.com/lyonmu/gopkg/version"
)

func main() {
	kong.Parse(&global.Cfg,
		kong.Name("ai-demo-go"),
		kong.Description("A simple AI demo application"),
		kong.UsageOnError(),
		kong.HelpOptions{
			Compact: true,
			Summary: true,
		},
	)
	if global.Cfg.Version {
		fmt.Println(version.Print("ai-demo-go"))
		os.Exit(0)
	}

	cmd.Run()
}
