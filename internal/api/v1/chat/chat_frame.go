package chat

import (
	"errors"

	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
)

// mapFrame 将一条 ChatResp 映射为下行 dtocode.Response，
// 生命周期由 Service 显式设置，不再根据 Token 数量推断结束帧。
// 返回的 bool 表示是否为错误帧；不向客户端暴露内部 error。
func mapFrame(v *dtochat.ChatResp) (dtocode.Response, bool) {
	resp := dtocode.SystemSuccess
	if v.Err != nil {
		resp = chatFailure(v.Err)
		chat := v.Chat
		chat.Flag = dtochat.ChatFlagError
		resp.Data = dtochat.ChatResp{Chat: chat}
		return resp, true
	}
	resp.Data = v
	return resp, false
}

// chatFailure 只把服务层哨兵错误映射为固定的用户可读响应；
// 其余内部错误保持通用提示，细节由服务端日志保留。
func chatFailure(err error) dtocode.Response {
	switch {
	case errors.Is(err, serviceagent.ErrConversationBusy):
		return dtocode.ChatBusy
	case errors.Is(err, serviceagent.ErrConversationNotFound):
		return dtocode.ConversationNotFound
	case errors.Is(err, serviceagent.ErrChatModelNotConfigured):
		return dtocode.ChatModelNotConfigured
	case errors.Is(err, serviceagent.ErrChatConcurrencyLimited):
		return dtocode.ChatConcurrencyLimited
	case errors.Is(err, serviceagent.ErrProviderSecretUnavailable):
		return dtocode.ProviderSecretUnusable
	default:
		return dtocode.ChatSSEFailure
	}
}
