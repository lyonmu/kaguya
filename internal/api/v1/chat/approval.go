package chat

import (
	"github.com/gin-gonic/gin"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
)

// ApprovalDecision
// @Tags Chat
// @Summary 批准或拒绝一次工具审批
// @Description 只有当前运行轮次中绑定匹配且未过期的待审批调用能产生新裁决；相同裁决重发只返回持久状态，不再次授权，冲突裁决返回 102013。批准不代表执行完成；只有消费事务成功提交后才允许本次工具调用，消费失败或提交不确定不执行。过期/取消不能重新批准。
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
// @Description 仅返回当前运行轮次仍有活执行入口、绑定匹配且未过期的待审批调用，包含安全展示参数与服务端 expires_at。仅补齐状态，不恢复已终结轮次、孤立审计行或跨进程审批。历史授权/执行结果由 turns.approvals 返回。
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
