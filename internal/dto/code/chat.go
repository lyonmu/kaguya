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
	// ChatConcurrencyLimited 为保留码：不再产生（聊天已无全局并发上限），
	// 新语义不得复用 102009。
	ChatConcurrencyLimited = Response{Code: 102009, Message: "同时进行的对话过多，请稍后重试"}
	ProviderSecretUnusable = Response{Code: 102010, Message: "提供商的 API Key 无法解密，请在 AI 提供商中重新填写"}

	// 会话权限与审批
	PermissionModeInvalid = Response{Code: 102011, Message: "会话权限模式无效"}
	ApprovalNotFound      = Response{Code: 102012, Message: "审批请求不存在或已结束"}
	ApprovalConflict      = Response{Code: 102013, Message: "审批已被其他操作处理，请刷新后重试"}
	ApprovalDenied        = Response{Code: 102014, Message: "工具调用被用户拒绝"}
	ApprovalExpired       = Response{Code: 102015, Message: "审批等待超时，工具调用已取消"}

	// 文件引用与上下文预算
	ReferenceInvalid            = Response{Code: 102016, Message: "引用路径无效或不在当前项目内"}
	ReferenceReadFailure        = Response{Code: 102017, Message: "引用内容读取失败"}
	ContextBudgetExceeded       = Response{Code: 102018, Message: "上下文预算不足，无法装入全部引用内容"}
	InstructionsUpgradeRequired = Response{Code: 102019, Message: "该会话仍使用旧指令快照，请先升级指令"}
)
