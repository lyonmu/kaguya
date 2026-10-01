package chat

import (
	"github.com/gin-gonic/gin"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
)

// ApprovalDecision
// @Tags Chat
// @Summary 批准或拒绝一次工具审批
// @Description 仅对当前运行轮次中仍等待裁决的审批有效；相同裁决重复请求幂等返回，冲突裁决返回 102013。批准只授权一次调用，参数哈希在登记后不可替换；过期、取消或已执行的审批不能再裁决。
// @Param id path string true "会话雪花 ID"
// @Param approval_id path string true "审批 ID"
// @Param data body dtochat.ApprovalDecisionReq true "approve 或 reject"
// @Success 200 {object} dtocode.Response{data=dtochat.ApprovalDecisionResp}
// @Router /v1/chat/conversation/{id}/approvals/{approval_id} [post]
func (b *ChatApiV1Group) ApprovalDecision(c *gin.Context) {
	var uri dtochat.ApprovalIDReq
	var req dtochat.ApprovalDecisionReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.DecideApproval(c.Request.Context(), uri.ID, uri.ApprovalID, req.Decision)
	if err != nil {
		conversationFailure(c, err, dtocode.ChatSSEFailure)
		return
	}
	c.Header("Cache-Control", "no-store")
	dtocode.SystemSuccess.Success(resp, c)
}

// ConversationApprovals
// @Tags Chat
// @Summary 当前等待裁决的审批列表
// @Description 仅作页面刷新后的状态补齐；不恢复已终结轮次，也不能跨进程复活审批。
// @Param id path string true "会话雪花 ID"
// @Success 200 {object} dtocode.Response{data=[]dtochat.Approval}
// @Router /v1/chat/conversation/{id}/approvals [get]
func (b *ChatApiV1Group) ConversationApprovals(c *gin.Context) {
	var uri dtochat.ConversationIDReq
	if err := c.ShouldBindUri(&uri); err != nil {
		dtocode.RequestParameterError.Failure(c)
		return
	}
	resp, err := agentvc.ConversationApprovals(c.Request.Context(), uri.ID)
	if err != nil {
		conversationFailure(c, err, dtocode.ConversationQueryFailure)
		return
	}
	c.Header("Cache-Control", "no-store")
	dtocode.SystemSuccess.Success(resp, c)
}
