package code

var (
	// 基础响应
	ChatSSEFailure            = Response{Code: 102000, Message: "流式对话失败"}
	ChatWSCanceled            = Response{Code: 102001, Message: "已取消"}
	ChatWSBusy                = Response{Code: 102002, Message: "上一轮对话尚未结束"}
	ConversationNotFound      = Response{Code: 102003, Message: "会话不存在或已删除"}
	ConversationQueryFailure  = Response{Code: 102004, Message: "查询会话失败"}
	ConversationUpdateFailure = Response{Code: 102005, Message: "更新会话失败"}
	ConversationDeleteFailure = Response{Code: 102006, Message: "删除会话失败"}
	TaskModelNotConfigured    = Response{Code: 102007, Message: "请先在 AI 提供商中配置任务模型"}
)
