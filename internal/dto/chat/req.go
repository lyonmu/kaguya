package chat

type ChatReq struct {
	Files     []string `json:"files,omitempty" binding:"max=8,dive,max=4096"`
	ProjectID string   `json:"project_id,omitempty" form:"project_id" binding:"max=64"` // 仅新对话使用
	ModelID   string   `json:"model_id,omitempty" form:"model_id"`                      // 本地模型记录 ID，空值使用全局默认模型
	ID        string   `json:"id,omitempty" form:"id"`                                  // 会话ID，空值表示开启新对话
	Messages  string   `json:"messages,omitempty" binding:"required" form:"messages"`
}
