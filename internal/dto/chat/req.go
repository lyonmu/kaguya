package chat

type ChatReq struct {
	ID       string `json:"id,omitempty" form:"id"` // 会话ID，空值表示开启新对话
	Flag     WSFlag `json:"flag" binding:"required"`
	Messages string `json:"messages,omitempty" binding:"required" form:"messages"`
}
