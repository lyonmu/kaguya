package config

// ProviderProtocol 定义模型服务提供方的 API 协议类型
type ProviderProtocol string

const (
	// ProtocolOpenAI 表示兼容 OpenAI 的 API 协议
	ProtocolOpenAI ProviderProtocol = "openai"
	// ProtocolAnthropic 表示 Anthropic 原生 API 协议
	ProtocolAnthropic ProviderProtocol = "anthropic"
)

// ReasoningEffort 定义模型思考/推理的努力程度
type ReasoningEffort string

const (
	// ReasoningEffortLow 低强度思考，响应更快但推理深度较浅
	ReasoningEffortLow ReasoningEffort = "low"
	// ReasoningEffortMedium 中等强度思考，平衡速度与推理深度
	ReasoningEffortMedium ReasoningEffort = "medium"
	// ReasoningEffortHigh 高强度思考，推理更深入但响应较慢
	ReasoningEffortHigh ReasoningEffort = "high"
)

// ReasoningInfo 模型思考/推理配置
type ReasoningInfo struct {
	// Enabled 是否启用思考模式
	Enabled bool `json:"enabled" yaml:"enabled" mapstructure:"enabled"`
	// Effort 思考努力程度，影响推理深度和响应速度
	Effort ReasoningEffort `json:"effort" yaml:"effort" mapstructure:"effort"`
}

// TokenInfo 模型 Token 相关限制配置
type TokenInfo struct {
	// ContextWindow 模型支持的最大上下文窗口大小（token 数）
	ContextWindow int `json:"context_window" yaml:"context_window" mapstructure:"context_window"`
	// MaxOutputTokens 模型单次生成的最大输出 token 数
	MaxOutputTokens int `json:"max_output_tokens" yaml:"max_output_tokens" mapstructure:"max_output_tokens"`
}

// ModelCapability 模型能力声明
type ModelCapability struct {
	// ToolUse 是否支持工具调用（function calling）
	ToolUse bool `json:"tool_use" yaml:"tool_use" mapstructure:"tool_use"`
	// Vision 是否支持图像理解
	Vision bool `json:"vision" yaml:"vision" mapstructure:"vision"`
	// StructuredOutput 是否支持结构化输出（如 JSON schema 约束）
	StructuredOutput bool `json:"structured_output" yaml:"structured_output" mapstructure:"structured_output"`
}

// ModelInfo 单个模型的完整配置信息
type ModelInfo struct {
	// Name 模型的显示名称
	Name string `json:"name" yaml:"name" mapstructure:"name"`
	// ID 调用 API 时使用的模型标识符
	ID string `json:"id" yaml:"id" mapstructure:"id"`
	// IsDefault 是否为该提供方下的默认模型
	IsDefault bool `json:"is_default" yaml:"is_default" mapstructure:"is_default"`
	// Reasoning 模型思考/推理配置
	Reasoning ReasoningInfo `json:"reasoning" yaml:"reasoning" mapstructure:"reasoning"`
	// Token 模型 Token 限制配置
	Token TokenInfo `json:"token" yaml:"token" mapstructure:"token"`
	// Capabilities 模型能力声明
	Capabilities ModelCapability `json:"capabilities" yaml:"capabilities" mapstructure:"capabilities"`
}

// ProviderInfo 模型服务提供方的完整配置
type ProviderInfo struct {
	// Name 提供方的显示名称
	Name string `json:"name" yaml:"name" mapstructure:"name"`
	// BaseURL 提供方 API 的基础地址
	BaseURL string `json:"base_url" yaml:"base_url" mapstructure:"base_url"`
	// APIKey 直接配置的 API 密钥（优先级低于 APIKeyEnv）
	APIKey string `json:"api_key" yaml:"api_key" mapstructure:"api_key"`
	// Protocol API 协议类型，支持 openai-compatible 和 anthropic
	Protocol ProviderProtocol `json:"protocol" yaml:"protocol" mapstructure:"protocol"`
	// Headers 自定义 HTTP 请求头，用于认证或路由等场景
	Headers map[string]string `json:"headers" yaml:"headers" mapstructure:"headers"`
	// Models 该提供方下可用的模型列表
	Models []ModelInfo `json:"models" yaml:"models" mapstructure:"models"`
}
