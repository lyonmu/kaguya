package config

type ThinkInfo struct {
	Enabled bool `json:"enabled" yaml:"enabled" mapstructure:"enabled"` // 是否启用思考过程
	Effort  int  `json:"effort" yaml:"effort" mapstructure:"effort"`    // 思考过程的努力程度，数值越大，思考过程越复杂，响应时间可能越长 1 low 2 medium 3 high
}

type TokenInfo struct {
	ContextTokens  int `json:"context_tokens" yaml:"context_tokens" mapstructure:"context_tokens"`    // 模型上下文窗口大小，单位为token
	ResponseTokens int `json:"response_tokens" yaml:"response_tokens" mapstructure:"response_tokens"` // 输出长度限制，单位为token
}

type ModelInfo struct {
	Name      string    `json:"name" yaml:"name" mapstructure:"name"`                   // 模型名称，通常用于展示和选择
	Id        string    `json:"id" yaml:"id" mapstructure:"id"`                         // 模型ID，通常用于API调用
	IsDefault bool      `json:"is_default" yaml:"is_default" mapstructure:"is_default"` // 是否默认模型
	Think     ThinkInfo `json:"think" yaml:"think" mapstructure:"think"`                // 模型思考过程配置
	Token     TokenInfo `json:"token" yaml:"token" mapstructure:"token"`                // 模型token配置
}

type ProviderInfo struct {
	Name     string      `json:"name" yaml:"name" mapstructure:"name"`             // 提供商名称，通常用于展示和选择
	BaseURL  string      `json:"base_url" yaml:"base_url" mapstructure:"base_url"` // 提供商API基础URL
	ApiKey   string      `json:"api_key" yaml:"api_key" mapstructure:"api_key"`    // 提供商API密钥
	Protocol int         `json:"protocol" yaml:"protocol" mapstructure:"protocol"` // 提供商协议类型，1 OpenAI兼容协议 2 Anthropic兼容协议
	Models   []ModelInfo `json:"models" yaml:"models" mapstructure:"models"`       // 提供商支持的模型列表
}
