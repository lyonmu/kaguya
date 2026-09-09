package chat

// ConversationContextResp 累计所有已完成轮次的 total_tokens（含跨模型），以最新轮次的模型窗口计算占比；不是实际上下文占用。
type ConversationContextResp struct {
	ConversationID   string   `json:"conversation_id"`
	TurnIndex        int64    `json:"turn_index"`
	ModelID          string   `json:"model_id"`
	ModelName        string   `json:"model_name"`
	ContextTokens    *int64   `json:"context_tokens"`
	ContextWindow    int      `json:"context_window"`
	EffectiveWindow  int64    `json:"effective_window"`
	WindowRatio      float64  `json:"window_ratio"`
	Percent          *float64 `json:"percent"`
	MaxWindowPercent *float64 `json:"max_window_percent"`
}
