package memory

import "time"

// 记忆管理接口 DTO；聊天工具接口走 internal/agent/memorytools 的窄 Reader。

// MemoryPageSaveReq 人工新建/编辑记忆页面。
type MemoryPageSaveReq struct {
	ScopeKey     string              `json:"scope_key" binding:"required,max=128"`
	Kind         string              `json:"kind" binding:"required,oneof=preference fact decision procedure lesson"`
	CanonicalKey string              `json:"canonical_key" binding:"omitempty,max=160"`
	Title        string              `json:"title" binding:"required,max=120"`
	Summary      string              `json:"summary" binding:"omitempty,max=300"`
	Body         string              `json:"body"`
	Aliases      []string            `json:"aliases" binding:"omitempty,max=12,dive,max=120"`
	ExpiresAt    *time.Time          `json:"expires_at"`
	Pinned       *bool               `json:"pinned"`
	UserLocked   *bool               `json:"user_locked"`
	RelatedIDs   []string            `json:"related_ids" binding:"omitempty,max=8,dive,max=64"`
	Source       *MemorySourceRefReq `json:"source"` // 用户选中文字的来源定位
	Reason       string              `json:"reason" binding:"omitempty,max=500"`
}

// MemorySourceRefReq 定位一次选中文字的来源；必须来自真实轮次投影。
type MemorySourceRefReq struct {
	ConversationID string `json:"conversation_id" binding:"required,max=64"`
	TurnID         string `json:"turn_id" binding:"required,max=64"`
	PartKey        string `json:"part_key" binding:"required,max=200"`
	Quote          string `json:"quote" binding:"required"`
}

// MemoryPageUpdateReq 带 expected_version 的编辑/置顶/锁定；
// 人工并发编辑返回明确冲突，而非最后写入者悄悄覆盖。
type MemoryPageUpdateReq struct {
	ExpectedVersion int64               `json:"expected_version" binding:"required,min=1"`
	Kind            string              `json:"kind" binding:"omitempty,oneof=preference fact decision procedure lesson"`
	CanonicalKey    string              `json:"canonical_key" binding:"omitempty,max=160"`
	Title           string              `json:"title" binding:"omitempty,max=120"`
	Summary         *string             `json:"summary" binding:"omitempty,max=300"`
	Body            *string             `json:"body"`
	Aliases         []string            `json:"aliases" binding:"omitempty,max=12,dive,max=120"`
	ExpiresAt       *time.Time          `json:"expires_at"`
	ClearExpiresAt  bool                `json:"clear_expires_at"`
	Pinned          *bool               `json:"pinned"`
	UserLocked      *bool               `json:"user_locked"`
	ScopeKey        string              `json:"scope_key" binding:"omitempty,max=128"` // 仅支持显式提升到 shared
	RelatedIDs      []string            `json:"related_ids" binding:"omitempty,max=8,dive,max=64"`
	Reason          string              `json:"reason" binding:"omitempty,max=500"`
	Source          *MemorySourceRefReq `json:"source"`
}

// MemoryPageListReq 分页、范围、状态、关键词查询。
type MemoryPageListReq struct {
	ScopeKey string `form:"scope_key" binding:"omitempty,max=128"`
	Status   string `form:"status" binding:"omitempty,oneof=proposed active conflicted stale archived deleted"`
	Keyword  string `form:"keyword" binding:"omitempty,max=200"`
	Pinned   *bool  `form:"pinned"`
	Page     int    `form:"page,default=1" binding:"min=1,max=1000000"`
	PageSize int    `form:"page_size,default=20" binding:"min=1,max=100"`
}

// MemoryPageResp 页面摘要（列表项）。
type MemoryPageResp struct {
	ID           string     `json:"id"`
	ScopeKey     string     `json:"scope_key"`
	Kind         string     `json:"kind"`
	CanonicalKey string     `json:"canonical_key"`
	Title        string     `json:"title"`
	Summary      string     `json:"summary"`
	Status       string     `json:"status"`
	Version      int64      `json:"version"`
	Pinned       bool       `json:"pinned"`
	UserLocked   bool       `json:"user_locked"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// MemoryPageListResp 分页列表。
type MemoryPageListResp struct {
	Total    int              `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
	Items    []MemoryPageResp `json:"items"`
}

// MemoryRevisionResp 修订摘要；正文提供版本对比所需内容。
type MemoryRevisionResp struct {
	Version   int64     `json:"version"`
	Actor     string    `json:"actor"`
	JobID     string    `json:"job_id,omitempty"`
	Reason    string    `json:"reason"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	Body      string    `json:"body"`
	Status    string    `json:"status"`
	ClaimKeys []string  `json:"claim_keys"`
	CreatedAt time.Time `json:"created_at"`
}

// MemoryPageDeleteReq 明确的删除语义：disable 停用可恢复；forget 删除记忆。
type MemoryPageDeleteReq struct {
	Mode string `form:"mode,default=disable" binding:"omitempty,oneof=disable forget"`
}

// MemoryExportReq 用户确认的 Markdown 导出范围。
type MemoryExportReq struct {
	ScopeKeys      []string `json:"scope_keys" binding:"omitempty,max=3,dive,max=128"`
	PageIDs        []string `json:"page_ids" binding:"omitempty,max=200,dive,max=64"`
	IncludeDeleted bool     `json:"include_deleted"`
}

// MemoryExportResp 导出的 Markdown 文本。
type MemoryExportResp struct {
	Filename string `json:"filename"`
	Markdown string `json:"markdown"`
}

// MemoryJobResp 任务状态、错误码与成本；不返回原始 Prompt。
type MemoryJobResp struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	ScopeKey       string     `json:"scope_key"`
	ConversationID string     `json:"conversation_id,omitempty"`
	Status         string     `json:"status"`
	Attempt        int        `json:"attempt"`
	ErrorCode      string     `json:"error_code,omitempty"`
	ErrorSummary   string     `json:"error_summary,omitempty"`
	Proposal       any        `json:"proposal,omitempty"` // needs_review 的有界 PatchPlan
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	InputTokens    int64      `json:"input_tokens"`
	OutputTokens   int64      `json:"output_tokens"`
	TotalTokens    int64      `json:"total_tokens"`
	Calls          int        `json:"calls"`
}

// MemoryJobListResp 任务分页。
type MemoryJobListResp struct {
	Total    int             `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
	Items    []MemoryJobResp `json:"items"`
}

// MemoryJobListReq 任务列表查询。
type MemoryJobListReq struct {
	Status   string `form:"status" binding:"omitempty,oneof=pending running succeeded retry_wait blocked needs_review failed canceled"`
	Page     int    `form:"page,default=1" binding:"min=1,max=1000000"`
	PageSize int    `form:"page_size,default=20" binding:"min=1,max=100"`
}

// MemoryCompileReq 立即整理已选择范围的待处理来源。
type MemoryCompileReq struct {
	ScopeKey string `json:"scope_key" binding:"required,max=128"`
}

// MemoryStatusResp 是记忆状态与独立的后台任务用量标签。
type MemoryStatusResp struct {
	Enabled            bool  `json:"enabled"`
	AutoCapture        bool  `json:"auto_capture"`
	ContextTokens      int   `json:"context_tokens"`
	PolicyEpoch        int64 `json:"policy_epoch"`
	PendingSources     int   `json:"pending_sources"`
	ActivePages        int   `json:"active_pages"`
	IndexNormalizer    int   `json:"index_normalizer"`
	TaskModelSet       bool  `json:"task_model_set"`
	BlockedJobs        int   `json:"blocked_jobs"`
	FailedJobs         int   `json:"failed_jobs"`
	ReviewJobs         int   `json:"review_jobs"`
	MemoryInputTokens  int64 `json:"memory_input_tokens"`
	MemoryOutputTokens int64 `json:"memory_output_tokens"`
	MemoryTotalTokens  int64 `json:"memory_total_tokens"`
	MemoryCalls        int   `json:"memory_calls"`
	MemoryUsageKnown   bool  `json:"memory_usage_known"`
}

// MemoryRefsResp 是一轮召回引用的解析结果；已删除页面显示“已删除”，
// 不能通过历史版本接口绕过删除。
type MemoryRefsResp struct {
	RetrieverVersion int                 `json:"retriever_version"`
	EstimatedTokens  int64               `json:"estimated_tokens"`
	Items            []MemoryRefItemResp `json:"items"`
}

// MemoryRefItemResp 是单条召回引用。
type MemoryRefItemResp struct {
	PageID  string `json:"page_id"`
	Version int64  `json:"version"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Deleted bool   `json:"deleted"`
}
