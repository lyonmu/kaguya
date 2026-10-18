package chat

// TurnReferenceSummary 记录本轮引用选择、包含范围与遗漏原因，供失败重试恢复引用意图与
// 前端审计。它只保存元信息与范围，不保存第二份正文。
type TurnReferenceSummary struct {
	PreparationState string                      `json:"preparation_state,omitempty" enums:"requested,resolved"`
	Sources          []TurnReferenceSource       `json:"sources,omitempty"`
	Warnings         []string                    `json:"warnings,omitempty"`
	ScanIncomplete   bool                        `json:"scan_incomplete,omitempty"`
	EstimatedTokens  int64                       `json:"estimated_tokens,omitempty"`
	BudgetKnown      *bool                       `json:"budget_known,omitempty"`
	InlineBytes      int64                       `json:"inline_bytes,omitempty"`
	PartialReason    string                      `json:"partial_reason,omitempty"`
	Continuation     []TurnReferenceContinuation `json:"continuation,omitempty"`
}

// TurnReferenceContinuation 是渐进探索提示，不是可回放扫描游标或工具授权。
// UnscannedSubtrees 保守覆盖遗漏范围，可能含已扫描部分；数量仅指已知遗漏。
type TurnReferenceContinuation struct {
	Root              string   `json:"root"`
	LastPath          string   `json:"last_stable_path,omitempty"`
	UnscannedSubtrees []string `json:"unscanned_subtrees,omitempty"`
	KnownOmittedFiles int      `json:"known_omitted_files,omitempty"`
	Reason            string   `json:"reason"`
}

// TurnReferenceSource 是一个显式文件或目录来源的解析结果。
type TurnReferenceSource struct {
	Type     string               `json:"type"` // file | directory
	Path     string               `json:"path"`
	Hash     string               `json:"hash,omitempty"`
	Included bool                 `json:"included,omitempty"`
	Ranges   []TurnReferenceRange `json:"ranges,omitempty"`
	Warnings []string             `json:"warnings,omitempty"`
	Files    []TurnReferenceFile  `json:"files,omitempty"`
}

// TurnReferenceFile 把每个来源关联到实际装入的文件片段；hash 不是整文件 hash。
// 旧字段保持兼容，未包含/空/二进制不生成虚假的行范围或内容指纹。
type TurnReferenceFile struct {
	Path         string               `json:"path"`
	ResolvedPath string               `json:"resolved_path,omitempty"`
	Hash         string               `json:"hash,omitempty"`
	Included     bool                 `json:"included"`
	Status       string               `json:"status"`
	Ranges       []TurnReferenceRange `json:"ranges,omitempty"`
	NextLine     int                  `json:"next_line,omitempty"`
}

// TurnReferenceRange 是实际装入上下文的行范围；后续内容由 read/find 渐进读取。
type TurnReferenceRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}
