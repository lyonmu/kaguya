package permission

import (
	"context"
	"fmt"

	"charm.land/fantasy"
)

// GatedTool 是一个注册工具及其可信分类。Classify 可选：只对需要按最终参数
// 动态判定（例如 shell AST）的工具提供，返回空 Identity 时沿用 Class。
type GatedTool struct {
	Tool     fantasy.AgentTool
	Class    Classification
	Classify func(call fantasy.ToolCall) Classification
}

// Wrapper 只装饰带审批语义的受限工具：Info/ProviderOptions/并行元数据全部
// 委托原工具，只有 Run 在策略要求时插入审批等待，不改变参数 schema 与取消语义。
type Wrapper struct {
	tool     fantasy.AgentTool
	class    Classification
	mode     Mode
	approver Approver
	classify func(call fantasy.ToolCall) Classification
}

// WrapAll 按当前模式装饰全部工具。mode 为 full_access 且没有动态分类需求时
// 直接返回原工具，保持既有行为与性能。
func WrapAll(mode Mode, approver Approver, tools []GatedTool) []fantasy.AgentTool {
	wrapped := make([]fantasy.AgentTool, 0, len(tools))
	for _, item := range tools {
		if item.Tool == nil {
			continue
		}
		if mode == ModeFullAccess && item.Classify == nil {
			wrapped = append(wrapped, item.Tool)
			continue
		}
		wrapped = append(wrapped, &Wrapper{tool: item.Tool, class: item.Class, mode: mode, approver: approver, classify: item.Classify})
	}
	return wrapped
}

func (w *Wrapper) Info() fantasy.ToolInfo                   { return w.tool.Info() }
func (w *Wrapper) ProviderOptions() fantasy.ProviderOptions { return w.tool.ProviderOptions() }
func (w *Wrapper) SetProviderOptions(opts fantasy.ProviderOptions) {
	w.tool.SetProviderOptions(opts)
}

// classification 计算本次调用的可信分类；动态分类返回空 Identity 时回退静态值。
func (w *Wrapper) classification(call fantasy.ToolCall) Classification {
	if w.classify == nil {
		return w.class
	}
	refined := w.classify(call)
	if refined.Identity == "" {
		refined.Identity = w.class.Identity
	}
	if refined.Effect == "" {
		refined.Effect = w.class.Effect
	}
	if !refined.ReadOnlyProven {
		refined.ReadOnlyProven = w.class.ReadOnlyProven
	}
	return refined
}

// Run 按策略放行或等待审批；拒绝、过期、取消都返回结构化错误结果，让模型
// 看到失败原因并继续，而不是断开整轮。批准后才调用原工具。
func (w *Wrapper) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	class := w.classification(call)
	if Decide(w.mode, class) == DecisionAllow {
		return w.tool.Run(ctx, call)
	}
	if w.approver == nil {
		return deniedResponse(OutcomeRejected, "", "approval is not available in this context"), nil
	}
	req := &Request{
		ToolCallID:   call.ID,
		ToolIdentity: class.Identity,
		ToolName:     toolName(w.tool),
		Effect:       class.Effect,
		Mode:         w.mode,
		Input:        call.Input,
		InputHash:    HashInput(call.Input),
	}
	outcome, err := w.approver.Await(ctx, req)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if outcome == OutcomeApproved {
		return w.tool.Run(ctx, call)
	}
	return deniedResponse(outcome, req.ApprovalID, ""), nil
}

// deniedResponse 构造被拒绝/过期/取消的工具错误结果，并附带结构化元数据。
func deniedResponse(outcome Outcome, approvalID, detail string) fantasy.ToolResponse {
	message := "Tool call was not executed."
	switch outcome {
	case OutcomeRejected:
		message = "Tool call was rejected by the user and was NOT executed. Do not retry the identical call; continue with a different approach or ask the user."
	case OutcomeExpired:
		message = "Tool call approval expired and was NOT executed. Do not retry silently; ask the user whether to try again."
	case OutcomeCanceled:
		message = "Tool call approval was canceled and was NOT executed."
	}
	if detail != "" {
		message += " " + detail
	}
	return WrapMetadata(fantasy.NewTextErrorResponse(message), Metadata{ApprovalID: approvalID, Outcome: string(outcome)})
}

func toolName(tool fantasy.AgentTool) string {
	if tool == nil {
		return ""
	}
	return tool.Info().Name
}

// String 只用于日志与排障，不包含参数。
func (w *Wrapper) String() string {
	return fmt.Sprintf("permission.Wrapper(%s/%s/%s)", w.class.Identity, w.class.Effect, w.mode)
}
