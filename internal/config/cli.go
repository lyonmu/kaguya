package config

type Cli struct {
	Version      bool           `name:"version" short:"v" long:"version" help:"Print version information and exit" default:"false"`
	Port         uint16         `name:"port" short:"p" long:"port" help:"Port to listen on" default:"9024"`
	Debug        bool           `name:"debug" short:"d" long:"debug" help:"Enable debug mode" default:"false"`
	MachineID    int            `name:"machine-id" short:"m" long:"machine-id" help:"Machine ID for the application" default:"1"`
	RouterPrefix string         `name:"router-prefix" short:"r" long:"router-prefix" help:"Router prefix for the application" default:"/kaguya/api"`
	DB           DatabaseConfig `embed:"" prefix:"db." mapstructure:"db" json:"db" yaml:"db"`
	LogInfo      LogConfig      `embed:"" prefix:"log." mapstructure:"log" json:"log" yaml:"log"`
}
