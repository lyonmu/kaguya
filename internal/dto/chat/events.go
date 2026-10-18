package chat

// 标题文本与任务状态分别使用独立版本，仅描述已提交的变更。
const (
	TitleEventUpdated    = "conversation.title.updated"
	TitleJobEventUpdated = "conversation.title.job.updated"
)

// TitleEvent 是 GET /v1/events 的下行载荷；revision 来自 title_revision，
// job_revision 来自任务行。status 保留旧协议兼容，不能代替任务版本。
type TitleEvent struct {
	Type           string `json:"type"`
	ConversationID string `json:"conversation_id"`
	Revision       int64  `json:"revision"`
	Title          string `json:"title,omitempty"`
	Status         string `json:"status,omitempty"`
	JobStatus      string `json:"job_status,omitempty"`
	JobRevision    *int64 `json:"job_revision,omitempty"`
}
