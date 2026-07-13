package config

import (
	"errors"
	"fmt"
	"strings"

	gopkgviper "github.com/lyonmu/gopkg/viper"
)

type Config struct {
	Providers []ProviderInfo `json:"provider_info" yaml:"provider_info" mapstructure:"provider_info"` // 提供商信息
}

// Load 从指定路径加载配置文件并校验，不创建 provider、不访问网络。
func Load(path string) (Config, error) {
	var cfg Config
	cm := gopkgviper.NewConfigManager(&cfg)
	defer cm.Close()
	if err := cm.LoadConfig(path, "yaml"); err != nil {
		return Config{}, fmt.Errorf("failed to load config: %w", err)
	}
	for i, provider := range cfg.Providers {
		if err := validateProvider(provider); err != nil {
			return Config{}, fmt.Errorf("provider[%d]: %w", i, err)
		}
	}
	return cfg, nil
}

// validateProvider 校验单个提供方的配置。
func validateProvider(p ProviderInfo) error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("provider name is required")
	}
	if p.Protocol != ProtocolOpenAI && p.Protocol != ProtocolAnthropic {
		return fmt.Errorf("unsupported protocol: %q", p.Protocol)
	}
	if strings.TrimSpace(p.BaseURL) == "" {
		return errors.New("base_url is required")
	}
	for i, model := range p.Models {
		if strings.TrimSpace(model.ID) == "" {
			return fmt.Errorf("model[%d]: model id is required", i)
		}
	}
	if _, err := p.DefaultModel(); err != nil {
		return err
	}
	return nil
}
