package chat

import "github.com/lyonmu/kaguya/internal/consts"

type ChatSSEResp struct {
	Chat        Chat                    `json:"chat"`         // 对话信息
	APIProtocol consts.ProviderProtocol `json:"api_protocol"` // api接口类型
	Usage       Usage                   `json:"usage"`        // token 使用信息
	Created     int64                   `json:"created"`      // 创建时间时间戳
	ModelID     string                  `json:"model_id"`     // 模型id
	ModelName   string                  `json:"model_name"`   // 模型名称
	IsError     bool                    `json:"is_error"`     // 是否产生错误需要进行终止
}
type Chat struct {
	ID      string `json:"id"`      // 对话id
	Content string `json:"content"` // 模型返回的内容
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`  // 输入 Token 数。
	OutputTokens int `json:"output_tokens"` // 输出 Token 数
	TotalTokens  int `json:"total_tokens"`  // 总 Token 数
	CachedTokens int `json:"cached_tokens"` // 命中缓存的 Token 数
}
