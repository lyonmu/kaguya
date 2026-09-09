package system

// SystemInfoSaveReq 整体保存系统配置；空模型 ID 表示取消选择。
type SystemInfoSaveReq struct {
	SystemPrompt   string `json:"system_prompt" binding:"max=20000"`
	UserAgent      string `json:"user_agent" binding:"required,max=512"`
	DefaultModelID string `json:"default_model_id" binding:"max=64"`
	TaskModelID    string `json:"task_model_id" binding:"max=64"`
}

type SystemInfoResp struct {
	SystemInfoSaveReq
	GlobalSystemPrompt string `json:"global_system_prompt"` // 只读基础人设，与自定义提示词拼接后用于聊天
}
