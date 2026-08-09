package chat

type ChatReq struct {
	ConversationID string `json:"conversation_id,omitempty" form:"conversation_id"` // 会话ID，空值表示开启新对话
	Messages       string `json:"messages,omitempty" binding:"required" form:"messages"`
}
