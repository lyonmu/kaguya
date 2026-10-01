package chat

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/global"
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
)

// mapChatError 是聊天域服务端错误的唯一映射入口：把服务层哨兵错误映射为
// 固定的用户可读响应；未识别的内部错误落到调用方给出的 operation fallback。
// 映射只依赖 backend code，HTTP 写出与 SSE 帧各自复用同一结果。
func mapChatError(err error, operation dtocode.Response) dtocode.Response {
	switch {
	case errors.Is(err, serviceagent.ErrConversationBusy):
		return dtocode.ChatBusy
	case errors.Is(err, serviceagent.ErrConversationNotFound):
		return dtocode.ConversationNotFound
	case errors.Is(err, serviceagent.ErrTaskModelNotConfigured):
		return dtocode.TaskModelNotConfigured
	case errors.Is(err, serviceagent.ErrChatModelNotConfigured):
		return dtocode.ChatModelNotConfigured
	case errors.Is(err, serviceagent.ErrProviderSecretUnavailable):
		return dtocode.ProviderSecretUnusable
	case errors.Is(err, serviceagent.ErrServiceStopping):
		return dtocode.ServiceStopping
	case errors.Is(err, serviceagent.ErrConversationUpdate):
		return dtocode.RequestParameterError
	case errors.Is(err, serviceagent.ErrPermissionModeInvalid):
		return dtocode.PermissionModeInvalid
	case errors.Is(err, serviceagent.ErrApprovalNotFound):
		return dtocode.ApprovalNotFound
	case errors.Is(err, serviceagent.ErrApprovalConflict):
		return dtocode.ApprovalConflict
	case errors.Is(err, serviceagent.ErrReferenceInvalid):
		return dtocode.ReferenceInvalid
	case errors.Is(err, serviceagent.ErrReferenceReadFailure):
		return dtocode.ReferenceReadFailure
	case errors.Is(err, serviceagent.ErrContextBudgetExceeded):
		return dtocode.ContextBudgetExceeded
	default:
		return operation
	}
}

// conversationFailure 处理非流式会话接口的失败响应。客户端取消是正常生命周期，
// 不写响应；未识别错误保留 operation fallback 并记录服务端日志。
func conversationFailure(c *gin.Context, err error, fallback dtocode.Response) {
	if errors.Is(err, context.Canceled) && errors.Is(c.Request.Context().Err(), context.Canceled) {
		return
	}
	resp := mapChatError(err, fallback)
	if resp == fallback {
		global.Logger.Sugar().Errorf("conversation API failed: %v", err)
	}
	resp.Failure(c)
}
