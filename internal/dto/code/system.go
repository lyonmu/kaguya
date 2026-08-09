package code

var (
	// 基础响应
	SystemSuccess = Response{Code: 100000, Message: "操作成功"}
	SystemFailure = Response{Code: 999999, Message: "操作失败"}

	// 系统通用响应
	RequestParameterError = Response{Code: 100001, Message: "请求参数错误"}

	// 操作日志相关
	AccessLogQueryFailure = Response{Code: 101000, Message: "访问日志查询失败"}
)
