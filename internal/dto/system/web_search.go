package system

type WebSearchConfig struct {
	Enabled       bool `json:"enabled"`
	KeyConfigured bool `json:"key_configured"`
}

type WebSearchSaveReq struct {
	Enabled *bool  `json:"enabled" binding:"required"`
	APIKey  string `json:"api_key,omitempty" binding:"max=2048"` // 留空保留已保存密钥
}
