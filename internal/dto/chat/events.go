package chat

// 标题事件类型：只描述已提交的标题更新，客户端按 conversation_id 合并。
const TitleEventUpdated = "conversation.title.updated"

// TitleEvent 是 GET /v1/events 的下行载荷；revision 来自会话 title_revision，
// 不是标题任务次数。status 仅在需要表达任务终态时出现。
type TitleEvent struct {
	Type           string `json:"type"`
	ConversationID string `json:"conversation_id"`
	Revision       int64  `json:"revision"`
	Title          string `json:"title,omitempty"`
	Status         string `json:"status,omitempty"`
}
