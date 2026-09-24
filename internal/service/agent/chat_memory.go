package agent

import (
	"context"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/agent/memorytools"
	"github.com/lyonmu/kaguya/internal/db"
	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/internal/service/memory"
)

// memoryTurn 是一轮的自动召回结果：临时上下文文本、冻结的选择元数据与降级状态。
// 自动召回失败时 unavailable=true，调用方必须明确显示非致命状态，
// 不能默默切成无记忆模式。
type memoryTurn struct {
	text           string
	refs           []dtomemory.TurnMemoryRef
	estimated      int64
	retriever      int
	enabled        bool
	unavailable    bool
	reader         memorytools.Reader
	conversationID string
	policyEpoch    int64
}

// InjectMemory 把自动召回资料作为一条标明“资料而非指令”的临时 user-role 消息，
// 附加在系统消息之后、其余消息之前。独立切片避免改写 compactor 持有的底层数组。
// 它是 sidecar，不是历史前缀：不写入指令快照，也不永久拼进 requestPrompt。
func InjectMemory(messages []fantasy.Message, text string) []fantasy.Message {
	if text == "" {
		return messages
	}
	i := 0
	for i < len(messages) && messages[i].Role == fantasy.MessageRoleSystem {
		i++
	}
	out := make([]fantasy.Message, 0, len(messages)+1)
	out = append(out, messages[:i]...)
	out = append(out, fantasy.NewUserMessage(text))
	out = append(out, messages[i:]...)
	return out
}

// prepareMemory 在每轮生成前进行一次本地检索，结合少量置顶卡片在预算内组装
// 临时上下文，并登记只读记忆工具。范围由数据库会话归属闭包绑定。
func (s *AgentSvc) prepareMemory(ctx context.Context, conversationID string, window int, query string) memoryTurn {
	svc := memory.NewService(db.EntClient, global.Logger)
	policy, err := memory.LoadPolicy(ctx, db.EntClient)
	if err != nil {
		global.Logger.Sugar().Warnf("load memory policy failed: conversation_id=%s err=%v", conversationID, err)
		return memoryTurn{unavailable: true}
	}
	convPolicy, err := memory.ResolveConversationPolicy(ctx, db.EntClient, conversationID, policy)
	if err != nil {
		global.Logger.Sugar().Warnf("resolve memory mode failed: conversation_id=%s err=%v", conversationID, err)
		return memoryTurn{unavailable: true}
	}
	if !policy.Enabled || !convPolicy.Recall {
		return memoryTurn{}
	}
	turn := memoryTurn{enabled: true, retriever: memory.RetrieverVersion, conversationID: conversationID, policyEpoch: policy.Epoch}
	// 私密/不记忆会话同时避免自动捕获和自动召回；工具也随召回一并关闭。
	scopes := memory.RecallScopes(convPolicy.ProjectID)
	turn.reader = memory.NewConversationReader(svc, scopes, conversationID, policy.Epoch)
	selection, err := svc.AutoRecall(ctx, memory.RecallOptions{
		Scopes: scopes, Query: query,
		ContextTokens: policy.ContextTokens, Window: window,
	})
	if err != nil {
		global.Logger.Sugar().Warnf("auto memory recall failed: conversation_id=%s err=%v", conversationID, err)
		turn.unavailable = true
		turn.reader = nil
		return turn
	}
	turn.text = selection.Text
	turn.refs = selection.Refs
	turn.estimated = selection.EstimatedTokens
	turn.retriever = selection.RetrieverVersion
	return turn
}

// prepareWithMemory 把自动召回 sidecar 的注入接在压缩器处理之后、模型调用之前；
// 每个 step 重新校验冻结选择的可用性。sidecar 只进入本次请求消息，不写入
// compactor 的历史记录、快照或指令快照，多 step 也不会重复累积。
func prepareWithMemory(compactor *contextCompactor, render func(context.Context) string) fantasy.PrepareStepFunction {
	return func(ctx context.Context, opts fantasy.PrepareStepFunctionOptions) (context.Context, fantasy.PrepareStepResult, error) {
		nextCtx, prepared, err := compactor.prepare(ctx, opts)
		if err != nil {
			return nextCtx, prepared, err
		}
		messages := prepared.Messages
		if messages == nil {
			// 窗口未知时现有 compactor 返回零值。
			messages = opts.Messages
		}
		prepared.Messages = InjectMemory(messages, render(ctx))
		return nextCtx, prepared, nil
	}
}

// selection 冻结本轮召回记录；不复制完整页面。
func (m memoryTurn) selection() *dtomemory.TurnMemorySelection {
	if len(m.refs) == 0 {
		return nil
	}
	return &dtomemory.TurnMemorySelection{
		RetrieverVersion: m.retriever, EstimatedTokens: m.estimated, Refs: m.refs,
	}
}

// renderTransient 按冻结选择重新渲染临时上下文：运行中被删除/禁用的页面
// 立即失效，后续请求不得继续附加已撤销页面。
func (m memoryTurn) renderTransient(ctx context.Context) string {
	if len(m.refs) == 0 {
		return ""
	}
	svc := memory.NewService(db.EntClient, global.Logger)
	if m.conversationID != "" {
		if err := svc.CheckConversationEpoch(ctx, m.conversationID, m.policyEpoch); err != nil {
			return ""
		}
	}
	text, _, err := svc.RenderTransient(ctx, m.refs, m.estimated)
	if err != nil {
		global.Logger.Sugar().Warnf("revalidate memory selection failed: err=%v", err)
		return ""
	}
	return text
}
