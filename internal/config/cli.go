package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

type Cli struct {
	Version      bool           `name:"version" short:"v" long:"version" help:"Print version information and exit" default:"false"`
	Port         uint16         `name:"port" short:"p" long:"port" help:"Port to listen on" default:"9024"`
	Host         string         `name:"host" help:"IP address to listen on; use 0.0.0.0 to allow remote access" default:"127.0.0.1"`
	TrustedHosts []string       `name:"trusted-host" help:"Additional exact HTTP hostnames allowed through a trusted reverse proxy"`
	Debug        bool           `name:"debug" short:"d" long:"debug" help:"Enable debug mode" default:"false"`
	MachineID    int            `name:"machine-id" short:"m" long:"machine-id" help:"Machine ID for the application" default:"1"`
	SecretKey    string         `name:"secret-key" long:"secret-key" env:"KAGUYA_SECRET_KEY" help:"Key material for encrypting stored provider API keys: 32 bytes as hex or base64; defaults to a key derived from the SQLCipher database key"`
	RouterPrefix string         `name:"router-prefix" short:"r" long:"router-prefix" help:"Router prefix for the application" default:"/kaguya/api"`
	DB           DatabaseConfig `embed:"" prefix:"db." mapstructure:"db" json:"db" yaml:"db"`
	LogInfo      LogConfig      `embed:"" prefix:"log." mapstructure:"log" json:"log" yaml:"log"`
}

func (c *Cli) Validate() error {
	if net.ParseIP(c.Host) == nil {
		return fmt.Errorf("--host must be a valid IPv4 or IPv6 address")
	}
	for _, host := range c.TrustedHosts {
		if host == "" || len(host) > 253 || strings.ContainsAny(host, ":/\\@?#* \t\r\n") {
			return fmt.Errorf("--trusted-host must be an exact hostname without a scheme, port or wildcard")
		}
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return fmt.Errorf("--trusted-host contains an invalid hostname label")
			}
			for _, ch := range label {
				if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
					return fmt.Errorf("--trusted-host must be an ASCII hostname")
				}
			}
		}
	}
	return nil
}

func (c *Cli) ListenAddress() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(int(c.Port)))
}
