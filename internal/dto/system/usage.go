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

// TokenUsageResp 的 Start/End、TotalTokens、Conversations、Peak* 只统计请求时间段；
// ActivityStart/ActivityEnd、Days 固定为最近一年；Models/Providers 固定统计全部历史。
type TokenUsageResp struct {
	Start                 string                  `json:"start"`                   // 请求时间段起点（UTC 自然日）
	End                   string                  `json:"end"`                     // 请求时间段终点（UTC 自然日）
	ActivityStart         string                  `json:"activity_start"`          // 活动日历起点，固定为一年零一天前的 UTC 自然日
	ActivityEnd           string                  `json:"activity_end"`            // 活动日历终点，固定为今天 UTC 自然日
	TotalTokens           int64                   `json:"total_tokens"`            // 请求时间段内累计 Token
	Conversations         int64                   `json:"conversations"`           // 请求时间段内活跃会话（去重）
	PeakTokens            int64                   `json:"peak_tokens"`             // 请求时间段内单日 Token 峰值
	PeakTokensDate        string                  `json:"peak_tokens_date"`        // 单日 Token 峰值所在 UTC 自然日
	PeakConversations     int64                   `json:"peak_conversations"`      // 请求时间段内单日会话峰值
	PeakConversationsDate string                  `json:"peak_conversations_date"` // 单日会话峰值所在 UTC 自然日
	Days                  []TokenUsageDay         `json:"days"`                    // 最近一年每日活动（UTC 自然日，已补零），不随请求时间段变化
	Models                []TokenUsageComposition `json:"models"`                  // 全部历史按模型聚合，用量倒序最多 10 项
	Providers             []TokenUsageComposition `json:"providers"`               // 全部历史按厂商聚合，用量倒序最多 10 项
}
