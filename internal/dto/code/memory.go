package code

var (
	// 记忆管理响应
	MemoryFailure         = Response{Code: 108000, Message: "记忆操作失败，请稍后重试"}
	MemoryQueryFailure    = Response{Code: 108001, Message: "查询记忆失败"}
	MemoryPageNotFound    = Response{Code: 108002, Message: "记忆不存在或已删除"}
	MemoryVersionConflict = Response{Code: 108003, Message: "记忆已被修改，请刷新后重试"}
	MemoryJobNotFound     = Response{Code: 108004, Message: "记忆任务不存在"}
	MemoryJobNotReview    = Response{Code: 108005, Message: "该任务不在待审状态"}
	MemoryDisabled        = Response{Code: 108006, Message: "长期记忆功能未启用"}
	MemorySourceNotFound  = Response{Code: 108007, Message: "记忆来源不存在或已清除"}
)
