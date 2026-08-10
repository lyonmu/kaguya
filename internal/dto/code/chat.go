package code

var (
	// 基础响应
	ChatSSEFailure = Response{Code: 102000, Message: "流式对话失败"}
	ChatWSCanceled = Response{Code: 102001, Message: "已取消"}
	ChatWSBusy     = Response{Code: 102002, Message: "上一轮对话尚未结束"}
)
