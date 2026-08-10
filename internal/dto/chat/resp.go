package chat

import "github.com/lyonmu/kaguya/internal/consts"

type WSFlag string

const (
	WSFlagChat   WSFlag = "chat"   // 上行：发起一轮对话
	WSFlagCancel WSFlag = "cancel" // 上行：取消当前生成
	WSFlagStart  WSFlag = "start"  // 下行：本轮首帧（会话 ID + 模型信息）
	WSFlagDelta  WSFlag = "delta"  // 下行：增量内容帧
	WSFlagDone   WSFlag = "done"   // 下行：本轮末帧（完整回答 + Usage）
	WSFlagError  WSFlag = "error"  // 下行：出错或被取消
)

type ChatResp struct {
	Chat        Chat                    `json:"chat"`         // 对话信息
	APIProtocol consts.ProviderProtocol `json:"api_protocol"` // api接口类型
	Usage       Usage                   `json:"usage"`        // token 使用信息
	Created     int64                   `json:"created"`      // 创建时间时间戳
	ModelID     string                  `json:"model_id"`     // 模型id
	ModelName   string                  `json:"model_name"`   // 模型名称
	Err         error                   `json:"err"`          // 错误信息
}
type Chat struct {
	ID      string `json:"id"`      // 对话id
	Content string `json:"content"` // 模型返回的内容
	Flag    WSFlag `json:"flag"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`  // 输入 Token 数。
	OutputTokens int `json:"output_tokens"` // 输出 Token 数
	TotalTokens  int `json:"total_tokens"`  // 总 Token 数
	CachedTokens int `json:"cached_tokens"` // 命中缓存的 Token 数
}
