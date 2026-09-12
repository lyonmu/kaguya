package code

var (
	// 基础响应
	ChatSSEFailure            = Response{Code: 102000, Message: "对话生成失败，请检查模型服务配置或稍后重试"}
	ChatBusy                  = Response{Code: 102002, Message: "上一轮对话尚未结束"}
	ConversationNotFound      = Response{Code: 102003, Message: "会话不存在或已删除"}
	ConversationQueryFailure  = Response{Code: 102004, Message: "查询会话失败"}
	ConversationUpdateFailure = Response{Code: 102005, Message: "更新会话失败"}
	ConversationDeleteFailure = Response{Code: 102006, Message: "删除会话失败"}
	TaskModelNotConfigured    = Response{Code: 102007, Message: "请先在系统配置中选择任务模型"}
	ChatModelNotConfigured    = Response{Code: 102008, Message: "请先在系统配置中选择默认模型，或在输入框选择模型"}
	ChatConcurrencyLimited    = Response{Code: 102009, Message: "同时进行的对话过多，请稍后重试"}
	ProviderSecretUnusable    = Response{Code: 102010, Message: "提供商的 API Key 无法解密，请在 AI 提供商中重新填写"}
)
