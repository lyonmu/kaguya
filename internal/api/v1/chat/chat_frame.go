package chat

import (
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
)

// mapFrame 将一条 ChatResp 映射为下行 dtocode.Response，
// 生命周期由 Service 显式设置，不再根据 Token 数量推断结束帧。
// 返回的 bool 表示是否为错误帧；不向客户端暴露内部 error。
func mapFrame(v *dtochat.ChatResp) (dtocode.Response, bool) {
	resp := dtocode.SystemSuccess
	if v.Err != nil {
		resp = mapChatError(v.Err, dtocode.ChatSSEFailure)
		chat := v.Chat
		chat.Flag = dtochat.ChatFlagError
		resp.Data = dtochat.ChatResp{Chat: chat}
		return resp, true
	}
	resp.Data = v
	return resp, false
}
