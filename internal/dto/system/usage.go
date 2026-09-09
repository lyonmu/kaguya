package system

// TokenUsageReq 使用秒级 Unix 时间戳，首尾秒包含；默认最近一年，最多跨 366 个 UTC 自然日。
type TokenUsageReq struct {
	StartTime int64 `form:"start_time" json:"start_time,omitempty" binding:"min=0,max=253402300799" minimum:"0" maximum:"253402300799" example:"1735689600"` // 开始时间（Unix 秒），0 或省略表示默认范围起点
	EndTime   int64 `form:"end_time" json:"end_time,omitempty" binding:"min=0,max=253402300799" minimum:"0" maximum:"253402300799" example:"1735948799"`     // 结束时间（Unix 秒，包含整个结束秒），0 或省略表示今天 UTC 日末
}
type TokenUsageDay struct {
	Date          string `json:"date"`
	TotalTokens   int64  `json:"total_tokens"`
	Conversations int64  `json:"conversations"`
}
type TokenUsageComposition struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ProviderID      string `json:"provider_id"`
	ProviderName    string `json:"provider_name"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	ReasoningTokens int64  `json:"reasoning_tokens"`
	CachedTokens    int64  `json:"cached_tokens"`
	TotalTokens     int64  `json:"total_tokens"`
}
type TokenUsageResp struct {
	Start                 string                  `json:"start"`
	End                   string                  `json:"end"`
	TotalTokens           int64                   `json:"total_tokens"`
	Conversations         int64                   `json:"conversations"`
	PeakTokens            int64                   `json:"peak_tokens"`
	PeakTokensDate        string                  `json:"peak_tokens_date"`
	PeakConversations     int64                   `json:"peak_conversations"`
	PeakConversationsDate string                  `json:"peak_conversations_date"`
	Days                  []TokenUsageDay         `json:"days"`
	Models                []TokenUsageComposition `json:"models"`
	Providers             []TokenUsageComposition `json:"providers"`
}
