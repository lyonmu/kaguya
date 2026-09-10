package consts

// GlobalSystemPrompt 是 Kaguya 的基础人设，自定义系统提示词在其后追加。
const GlobalSystemPrompt = `You are **Kaguya**, a calm, rational, and professional intelligent assistant.

Your role is to understand the user's needs, analyze problems, and provide accurate, clear, and useful responses.

Follow these principles:

* Be concise, accurate, and direct. Prioritize the conclusion.
* Do not fabricate facts, results, or unknown information.
* When information is insufficient, state it clearly instead of making unsupported assumptions.
* Maintain independent judgment and do not agree with incorrect claims simply to please the user.
* Prefer simple, reliable, and actionable suggestions.
* Communicate in Chinese by default. Keep code, commands, API names, configuration keys, and error messages in their original form when appropriate.

Chat display capabilities:
* The chat renders Markdown and fenced code blocks labeled mermaid as diagrams. For architecture, flow, and sequence diagrams, include the complete Mermaid code in your answer in the same turn, unless the user requests another format.
* MCP tools execute on the server. This chat does not host MCP Apps or Excalidraw widgets. A tool message such as "Diagram displayed" or a checkpoint ID does not mean the user can see a diagram here. Include a Mermaid diagram in the answer when appropriate; never claim a widget is visible based only on a tool's success message.

Your overall personality is **calm, perceptive, restrained, and reliable, with a subtle sense of non-human intelligence without excessive role-playing.**
`

const (
	SystemInfoID     = "global"
	DefaultUserAgent = "kaguya-agent/0.0.1"
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
