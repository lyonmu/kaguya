package chat

type ChatReq struct {
	Files          []string    `json:"files,omitempty" binding:"max=8,dive,max=4096"`
	References     []Reference `json:"references,omitempty" binding:"dive"`
	ProjectID      string      `json:"project_id,omitempty" form:"project_id" binding:"max=64"` // 仅新对话使用
	ModelID        string      `json:"model_id,omitempty" form:"model_id"`                      // 本地模型记录 ID，空值使用全局默认模型
	ID             string      `json:"id,omitempty" form:"id"`                                  // 会话ID，空值表示开启新对话
	Messages       string      `json:"messages,omitempty" binding:"required" form:"messages"`
	PermissionMode string      `json:"permission_mode,omitempty" binding:"omitempty,oneof=full_approval auto_approval full_access"` // 仅新对话使用；续聊以数据库为准，不一致时拒绝
}

// Reference 是一次显式引用选择；path 为工作区相对路径，name 仅作展示。
// 后端不信任 metadata，也不接受绝对路径或跨工作区路径。
type Reference struct {
	Type string `json:"type" binding:"required,oneof=file directory"`
	Path string `json:"path" binding:"required,max=4096"`
	Name string `json:"name,omitempty" binding:"max=256"`
}
