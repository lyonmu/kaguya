package consts

type Status int

const (
	IsUnknown Status = -1
	IsTrue    Status = 1
	IsFalse   Status = 2
)

type DBKind string

const (
	MySQL  DBKind = "mysql"
	SQLite DBKind = "sqlite"
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
