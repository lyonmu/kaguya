package config

type Cli struct {
	Version bool `name:"version" short:"v" long:"version" help:"Print version information and exit" default:"false"`
}
