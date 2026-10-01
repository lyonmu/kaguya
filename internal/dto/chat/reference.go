package chat

// TurnReferenceSummary 记录本轮引用选择、包含范围与遗漏原因，供失败重试恢复引用意图与
// 前端审计。它只保存元信息与范围，不保存第二份正文。
type TurnReferenceSummary struct {
	Sources         []TurnReferenceSource `json:"sources,omitempty"`
	Warnings        []string              `json:"warnings,omitempty"`
	ScanIncomplete  bool                  `json:"scan_incomplete,omitempty"`
	EstimatedTokens int64                 `json:"estimated_tokens,omitempty"`
}

// TurnReferenceSource 是一个显式文件或目录来源的解析结果。
type TurnReferenceSource struct {
	Type     string               `json:"type"` // file | directory
	Path     string               `json:"path"`
	Hash     string               `json:"hash,omitempty"`
	Included bool                 `json:"included,omitempty"`
	Ranges   []TurnReferenceRange `json:"ranges,omitempty"`
	Warnings []string             `json:"warnings,omitempty"`
}

// TurnReferenceRange 是实际装入上下文的行范围；后续内容由 read/find 渐进读取。
type TurnReferenceRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}
