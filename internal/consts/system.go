package consts

// GlobalSystemPrompt 是 Kaguya 的基础人设，自定义系统提示词在其后追加。
const GlobalSystemPrompt = "你是 Kaguya（辉夜），一个安静、理性、可控的 AI 助手。清晰简洁地帮助用户，诚实说明不确定性，尊重用户的意图与决定。"

const (
	SystemInfoID     = "global"
	DefaultUserAgent = "kaguya"
)

type Status int

const (
	IsUnknown Status = -1
	IsTrue    Status = 1
	IsFalse   Status = 2
)

type DBKind string

const (
	MySQL      DBKind = "mysql"
	SQLite     DBKind = "sqlite"
	PostgreSQL DBKind = "postgresql"
	Postgres   DBKind = "postgres"
)

// ProviderProtocol 定义模型服务提供方的 API 协议类型
type ProviderProtocol string

const (
	// ProtocolOpenAI 表示 OpenAI 的 Chat Completions API 协议
	ProtocolOpenAIChat ProviderProtocol = "openai-chat"
	// ProtocolAnthropic 表示 Anthropic 的 Messages API 协议
	ProtocolAnthropic ProviderProtocol = "anthropic"
	// ProtocolOpenAIRespone 表示 OpenAI 的 OpenAI Responses API
	ProtocolOpenAIResponses ProviderProtocol = "openai-response"
)

// ProviderType 定义协议之外的提供商特定请求行为。
type ProviderType string

const (
	ProviderTypeNormal     ProviderType = "normal"
	ProviderTypeOpenCodeGo ProviderType = "opencode-go"
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
