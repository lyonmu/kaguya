package config

type Config struct {
	Providers []ProviderInfo `json:"provider_info" yaml:"provider_info" mapstructure:"provider_info"` // 提供商信息
}
