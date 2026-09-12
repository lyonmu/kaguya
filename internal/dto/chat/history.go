package chat

import "time"

type ConversationIDReq struct {
	ID string `uri:"id" binding:"required,max=64"`
}
type ConversationPageReq struct {
	IsProject *bool  `form:"is_project"` // true 仅项目对话；false 仅普通对话；未提供时有 project_id 则按项目，否则仅普通对话
	ProjectID string `form:"project_id" binding:"max=64"`
	Keyword   string `form:"keyword" binding:"max=200"` // 标题前缀搜索（走索引）
	Favorite  *bool  `form:"favorite"`
	Page      int    `form:"page,default=1" binding:"min=1,max=1000000"`
	PageSize  int    `form:"page_size,default=20" binding:"min=1,max=100"`
}
type ConversationUpdateReq struct {
	Title    *string `json:"title" binding:"omitempty,min=1,max=200"`
	Favorite *bool   `json:"favorite"`
}

type ConversationTitleResp struct {
	ID    string `json:"id"`
	Title string `json:"title"` // 当前已保存标题；生成失败或等待超时保留原标题
}

// TurnPageReq 按轮次分页；compact 模式延迟加载工具与思考详情，返回值始终按时间正序。
type TurnPageReq struct {
	Page    int   `form:"page" binding:"min=0,max=1000000"` // 1 起按时间正序分页；0 使用原有 before 游标，与 before 互斥
	Before  int64 `form:"before" binding:"min=0"`           // 上页 next_before；0 表示最新
	Limit   int   `form:"limit,default=5" binding:"min=1,max=100"`
	Compact bool  `form:"compact"` // 折叠的工具/思考内容由详情接口按需加载
}

type BlockDetailReq struct {
	ID        string `uri:"id" binding:"required,max=64"`
	TurnIndex int64  `uri:"turn" binding:"min=1"`
	Sequence  int64  `uri:"sequence" binding:"min=1"`
}
type ConversationResp struct {
	IsProject     bool      `json:"is_project"` // 根据 project_id 是否为空派生，不单独存储
	ProjectID     *string   `json:"project_id"`
	ID            string    `json:"id"`
	Title         string    `json:"title"` // AI 标题最多20字符；新会话创建后并行生成并异步写入，仍为“新对话”时由前端 POST title/wait 补生成或重试
	Favorite      bool      `json:"favorite"`
	TurnCount     int64     `json:"turn_count"`
	ModelID       string    `json:"model_id"`
	ModelName     string    `json:"model_name"`
	CreatedAt     time.Time `json:"created_at"`
	LastMessageAt time.Time `json:"last_message_at"`
	DurationMS    int64     `json:"duration_ms"`
	ToolCalls     int64     `json:"tool_calls"`
	Usage         Usage     `json:"usage"` // 仅累计已完成轮次
}
type ConversationListResp struct {
	Items    []ConversationResp `json:"items"`
	Total    int                `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
}

// StoredBlock 不复用流式 phase；工具输入/输出归在同一行，DetailsDeferred 标记仅含元信息的块。
type StoredBlock struct {
	DetailsDeferred  bool        `json:"details_deferred,omitempty"`
	HasOutput        bool        `json:"has_output,omitempty"`
	Sequence         int64       `json:"sequence"` // 轮内展示顺序；与 turn_index 联合排序
	Type             BlockType   `json:"type"`
	Text             string      `json:"text,omitempty"`
	ToolCallID       string      `json:"tool_call_id,omitempty"`
	ToolName         string      `json:"tool_name,omitempty"`
	Input            string      `json:"input,omitempty"`
	Output           *ToolOutput `json:"output,omitempty"`
	ProviderExecuted bool        `json:"provider_executed,omitempty"`
	IsError          bool        `json:"is_error,omitempty"`
	ErrorMessage     string      `json:"error_message,omitempty"`
	StartedAt        time.Time   `json:"started_at"`
	FinishedAt       time.Time   `json:"finished_at"`
	StartOrder       int64       `json:"start_order"` // Trace 按事件序号还原并行开始/结束顺序
	EndOrder         int64       `json:"end_order"`
}
type StoredTurn struct {
	TurnIndex    int64         `json:"turn_index"` // 仅历史分页/排序使用，不改变实时 DTO
	UserContent  string        `json:"user_content"`
	ProviderName string        `json:"provider_name"`
	ModelID      string        `json:"model_id"`
	ModelName    string        `json:"model_name"`
	APIProtocol  string        `json:"api_protocol"`
	StartedAt    time.Time     `json:"started_at"`
	FinishedAt   time.Time     `json:"finished_at"`
	DurationMS   int64         `json:"duration_ms"`
	ToolCalls    int64         `json:"tool_calls"`
	FinishReason string        `json:"finish_reason"`
	Status       string        `json:"status"` // running/completed/interrupted/failed；只有 completed 是完整上下文
	Usage        Usage         `json:"usage"`
	Blocks       []StoredBlock `json:"blocks"`
}
type TurnListResp struct {
	Total      int64        `json:"total"`
	Page       int          `json:"page"` // 页码模式为实际页码；游标模式为 0
	PageSize   int          `json:"page_size"`
	TotalPages int          `json:"total_pages"`
	Items      []StoredTurn `json:"items"` // 按 turn_index 升序，每轮 blocks 按 sequence 升序
	HasMore    bool         `json:"has_more"`
	NextBefore int64        `json:"next_before"` // 向前加载更早轮次的游标
}
