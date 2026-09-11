package code

var (
	// 项目管理相关
	ProjectOperationFailure = Response{Code: 107000, Message: "项目操作失败"}
	ProjectNotFound         = Response{Code: 107001, Message: "项目不存在或已删除"}
	ProjectParameterError   = Response{Code: 107002, Message: "项目参数无效，目录必须位于运行用户主目录内且可访问"}
	ProjectPathInUse        = Response{Code: 107003, Message: "该目录已被其他项目使用，请选择不同的目录"}
	ProjectNotGit           = Response{Code: 107004, Message: "当前项目不是 Git 仓库，无法查看代码改动"}
)
