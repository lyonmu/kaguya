package code

var (
	// 基础响应
	SystemSuccess = Response{Code: 100000, Message: "操作成功"}
	SystemFailure = Response{Code: 999999, Message: "操作失败"}

	// 系统通用响应
	RequestParameterError = Response{Code: 100001, Message: "请求参数错误"}

	SystemInfoQueryFailure  = Response{Code: 104000, Message: "系统配置查询失败"}
	SystemInfoUpdateFailure = Response{Code: 104001, Message: "系统配置保存失败"}

	// 提供商管理相关
	ProviderQueryFailure     = Response{Code: 106000, Message: "提供商查询失败"}
	ProviderNotFound         = Response{Code: 106001, Message: "提供商不存在"}
	ProviderCreateFailure    = Response{Code: 106002, Message: "提供商创建失败"}
	ProviderUpdateFailure    = Response{Code: 106003, Message: "提供商更新失败"}
	ProviderDeleteFailure    = Response{Code: 106004, Message: "提供商删除失败"}
	ProviderNameAlreadyExist = Response{Code: 106005, Message: "提供商名称已存在"}
	ProviderSecretInvalid   = Response{Code: 106006, Message: "API Key 加解密失败，请重新填写"}

	// MCP 管理相关
	MCPFailure        = Response{Code: 105000, Message: "MCP 操作失败"}
	MCPNotFound       = Response{Code: 105001, Message: "MCP 服务不存在"}
	MCPDuplicate      = Response{Code: 105002, Message: "MCP 服务名称已存在"}
	MCPConnectFailure = Response{Code: 105003, Message: "MCP 连接或工具发现失败，请检查服务配置"}
	MCPConfigInvalid  = Response{Code: 105004, Message: "MCP 配置无效"}

	// 模型管理相关
	ModelQueryFailure   = Response{Code: 103000, Message: "模型查询失败"}
	ModelNotFound       = Response{Code: 103001, Message: "模型不存在"}
	ModelCreateFailure  = Response{Code: 103002, Message: "模型创建失败"}
	ModelUpdateFailure  = Response{Code: 103003, Message: "模型更新失败"}
	ModelDeleteFailure  = Response{Code: 103004, Message: "模型删除失败"}
	ModelIDAlreadyExist = Response{Code: 103005, Message: "该提供商下模型标识已存在"}
)
