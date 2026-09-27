package memory

import "time"

// 记忆管理接口 DTO；聊天工具接口走 internal/agent/memorytools 的窄 Reader。

// MemoryPageSaveReq 人工新建/编辑记忆页面。
type MemoryPageSaveReq struct {
	ScopeKey      string              `json:"scope_key" binding:"required,max=128"`
	Kind          string              `json:"kind" binding:"required,oneof=preference fact decision procedure lesson"`
	CanonicalKey  string              `json:"canonical_key" binding:"omitempty,max=160"`
	Title         string              `json:"title" binding:"required"`
	Summary       string              `json:"summary" binding:"omitempty"`
	Body          string              `json:"body"`
	Aliases       []string            `json:"aliases" binding:"omitempty,dive,required"`
	ExpiresAt     *time.Time          `json:"expires_at"`
	Pinned        *bool               `json:"pinned"`
	UserLocked    *bool               `json:"user_locked"`
	RelatedIDs    []string            `json:"related_ids" binding:"omitempty,dive,max=64"`
	SupersedesIDs []string            `json:"supersedes_ids" binding:"omitempty,dive,max=64"` // 显式替代关系
	Source        *MemorySourceRefReq `json:"source"`                                         // 用户选中文字的来源定位
	Reason        string              `json:"reason" binding:"omitempty"`
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
	Title           string              `json:"title" binding:"omitempty"`
	Summary         *string             `json:"summary" binding:"omitempty"`
	Body            *string             `json:"body"`
	Aliases         []string            `json:"aliases" binding:"omitempty,dive,required"`
	ExpiresAt       *time.Time          `json:"expires_at"`
	ClearExpiresAt  bool                `json:"clear_expires_at"`
	Pinned          *bool               `json:"pinned"`
	UserLocked      *bool               `json:"user_locked"`
	ScopeKey        string              `json:"scope_key" binding:"omitempty,max=128"` // 仅支持显式提升到 shared
	RelatedIDs      []string            `json:"related_ids" binding:"omitempty,dive,max=64"`
	SupersedesIDs   []string            `json:"supersedes_ids" binding:"omitempty,dive,max=64"` // 显式替代关系
	Reason          string              `json:"reason" binding:"omitempty"`
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
	ID             string             `json:"id"`
	Kind           string             `json:"kind"`
	ScopeKey       string             `json:"scope_key"`
	ConversationID string             `json:"conversation_id,omitempty"`
	Status         string             `json:"status"`
	Attempt        int                `json:"attempt"`
	ErrorCode      string             `json:"error_code,omitempty"`
	ErrorSummary   string             `json:"error_summary,omitempty"`
	Proposal       any                `json:"proposal,omitempty"` // needs_review 的有界 PatchPlan
	Progress       *MemoryJobProgress `json:"progress,omitempty"` // backfill 进度
	CreatedAt      time.Time          `json:"created_at"`
	StartedAt      *time.Time         `json:"started_at,omitempty"`
	FinishedAt     *time.Time         `json:"finished_at,omitempty"`
	InputTokens    int64              `json:"input_tokens"`
	OutputTokens   int64              `json:"output_tokens"`
	TotalTokens    int64              `json:"total_tokens"`
	Calls          int                `json:"calls"`
}

// MemoryJobProgress 是 backfill 作业的扫描进度；成本上限达到时 limited=true。
type MemoryJobProgress struct {
	Scanned  int  `json:"scanned"`
	Created  int  `json:"created"`
	Skipped  int  `json:"skipped"`
	Limited  bool `json:"limited"`
	Finished bool `json:"finished"`
}

// MemoryBackfillReq 用户显式选择的历史回填；必须给出范围与成本上限。
type MemoryBackfillReq struct {
	ScopeKey       string     `json:"scope_key" binding:"required,max=128"`
	ConversationID string     `json:"conversation_id" binding:"omitempty,max=64"`
	After          *time.Time `json:"after"`
	Before         *time.Time `json:"before"`
	MaxSources     int        `json:"max_sources" binding:"min=1,max=100000"`
}

// MemoryBackfillResp 是回填作业的进度；重复请求返回同一进行中作业。
type MemoryBackfillResp struct {
	JobID      string `json:"job_id"`
	ScopeKey   string `json:"scope_key"`
	Status     string `json:"status"`
	Scanned    int    `json:"scanned"`
	Created    int    `json:"created"`
	Skipped    int    `json:"skipped"`
	MaxSources int    `json:"max_sources"`
	Limited    bool   `json:"limited"`
	Finished   bool   `json:"finished"`
	ErrorCode  string `json:"error_code,omitempty"`
}

// MemoryImportReq 小范围项目资料导入；路径由服务端复用项目路径校验与忽略规则。
type MemoryImportReq struct {
	ScopeKey string `json:"scope_key" binding:"required,max=128"`
	Path     string `json:"path" binding:"required,max=4096"`
}

// MemoryImportResp 是导入来源的状态；同路径同内容重复导入返回已存在来源。
type MemoryImportResp struct {
	SourceID     string `json:"source_id"`
	State        string `json:"state"`
	Path         string `json:"path"`
	Size         int64  `json:"size"`
	Deduplicated bool   `json:"deduplicated"`
}

// MemorySourceListReq 来源列表；用于导入/回填状态与错误追踪。
type MemorySourceListReq struct {
	ScopeKey string `form:"scope_key" binding:"omitempty,max=128"`
	Kind     string `form:"kind" binding:"omitempty,oneof=turn note import"`
	State    string `form:"state" binding:"omitempty,oneof=pending claimed processed noop failed excluded"`
	Page     int    `form:"page,default=1" binding:"min=1,max=1000000"`
	PageSize int    `form:"page_size,default=20" binding:"min=1,max=100"`
}

// MemorySourceItemResp 是来源列表项摘要。
type MemorySourceItemResp struct {
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	ScopeKey       string    `json:"scope_key"`
	State          string    `json:"state"`
	SourceKey      string    `json:"source_key"`
	ConversationID string    `json:"conversation_id,omitempty"`
	TurnID         string    `json:"turn_id,omitempty"`
	DocumentPath   string    `json:"document_path,omitempty"`
	ContentHash    string    `json:"content_hash,omitempty"`
	JobID          string    `json:"job_id,omitempty"`
	PolicyEpoch    int64     `json:"policy_epoch"`
	CapturedAt     time.Time `json:"captured_at"`
}

// MemorySourceListResp 来源分页。
type MemorySourceListResp struct {
	Total    int                    `json:"total"`
	Page     int                    `json:"page"`
	PageSize int                    `json:"page_size"`
	Items    []MemorySourceItemResp `json:"items"`
}

// MemorySourcePartResp 是来源片段的有限展示；正文只返回截断后的安全文本。
type MemorySourcePartResp struct {
	PartKey   string `json:"part_key"`
	Origin    string `json:"origin"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// MemorySourceDetailResp 是来源导航结果：从 Memory 定位到轮次/笔记/导入资料。
type MemorySourceDetailResp struct {
	MemorySourceItemResp
	Available         bool                   `json:"available"`
	UnavailableReason string                 `json:"unavailable_reason,omitempty"`
	TurnStatus        string                 `json:"turn_status,omitempty"`
	FinishReason      string                 `json:"finish_reason,omitempty"`
	Parts             []MemorySourcePartResp `json:"parts"`
}

// MemoryDiffResp 是任意两个修订的结构化对比；顺序稳定、可解释。
type MemoryDiffResp struct {
	PageID          string                     `json:"page_id"`
	From            MemoryDiffSideResp         `json:"from"`
	To              MemoryDiffSideResp         `json:"to"`
	ContentChanges  []MemoryFieldChangeResp    `json:"content_changes"`
	MetadataChanges []MemoryFieldChangeResp    `json:"metadata_changes"`
	ClaimChanges    []MemoryClaimChangeResp    `json:"claim_changes"`
	EvidenceChanges []MemoryEvidenceChangeResp `json:"evidence_changes"`
}

// MemoryDiffSideResp 是版本对比的一侧完整快照。
type MemoryDiffSideResp struct {
	Version   int64     `json:"version"`
	Actor     string    `json:"actor"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	Body      string    `json:"body"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	Aliases   []string  `json:"aliases"`
}

// MemoryFieldChangeResp 是字段级变化。
type MemoryFieldChangeResp struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// MemoryClaimChangeResp 是主张级变化。
type MemoryClaimChangeResp struct {
	Key       string `json:"key"`
	Change    string `json:"change"` // added / removed / changed
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	FromBasis string `json:"from_basis,omitempty"`
	ToBasis   string `json:"to_basis,omitempty"`
}

// MemoryEvidenceChangeResp 是证据级变化。
type MemoryEvidenceChangeResp struct {
	ClaimKey string `json:"claim_key"`
	Change   string `json:"change"` // added / removed / changed
	SourceID string `json:"source_id"`
	PartKey  string `json:"part_key"`
	Quote    string `json:"quote,omitempty"`
	Relation string `json:"relation,omitempty"`
	Basis    string `json:"basis,omitempty"`
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
