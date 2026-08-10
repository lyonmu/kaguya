package chat

type WSFlag string

const (
	WSFlagChat   WSFlag = "chat"   // 上行：发起一轮对话
	WSFlagCancel WSFlag = "cancel" // 上行：取消当前生成
	WSFlagStart  WSFlag = "start"  // 下行：本轮首帧（会话 ID + 模型信息）
	WSFlagDelta  WSFlag = "delta"  // 下行：增量内容帧
	WSFlagDone   WSFlag = "done"   // 下行：本轮末帧（完整回答 + Usage）
	WSFlagError  WSFlag = "error"  // 下行：出错或被取消
)

type ChatWSReq struct {
	Flag           WSFlag `json:"flag" binding:"required"`
	ConversationID string `json:"conversation_id,omitempty"`
	Messages       string `json:"messages,omitempty"`
}

type ChatWSResp struct {
	Flag WSFlag      `json:"flag"`
	Data ChatSSEResp `json:"data,omitempty"`
}
