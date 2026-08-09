package chat

type ChatReq struct {
	Messages string `json:"messages,omitempty" binding:"required" form:"messages"`
}
