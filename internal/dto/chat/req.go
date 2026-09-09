package chat

type ChatReq struct {
	ModelID  string `json:"model_id,omitempty" form:"model_id"` // 本地模型记录 ID，空值使用全局默认模型
	ID       string `json:"id,omitempty" form:"id"`             // 会话ID，空值表示开启新对话
	Flag     WSFlag `json:"flag" binding:"required"`
	Messages string `json:"messages,omitempty" binding:"required" form:"messages"`
}
