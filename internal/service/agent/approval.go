package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lyonmu/kaguya/internal/agent/permission"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaapproval"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/global"
)

// approvalTimeout 是单次工具审批的最长等待；超时后工具不执行，模型收到结构化失败。
// 变量形式便于测试注入短超时，生产默认 10 分钟。
var approvalTimeout = 10 * time.Minute

var (
	// ErrApprovalNotFound 表示审批不存在、不属于该会话或当前进程无法裁决。
	ErrApprovalNotFound = errors.New("approval not found")
	// ErrApprovalConflict 表示审批已被其他裁决处理或已过期/执行。
	ErrApprovalConflict = errors.New("approval already resolved with a different decision")
)

// approvalBroker 是单轮对话的审批等待器，实现 permission.Approver。
// 每个 pending 都先持久化为审计行再注册裁决入口，等待期间不持有任何工具、
// 文件或 MCP 连接锁。
type approvalBroker struct {
	conversationID string
	turnID         string
	emitter        *TurnEmitter

	mu      sync.Mutex
	closed  bool
	pending map[string]*approvalWaiter
}

type approvalWaiter struct {
	outcome chan permission.Outcome
}

// registeredApproval 是裁决接口访问当前进程等待中的审批的入口。
type registeredApproval struct {
	broker   *approvalBroker
	approval dtochat.Approval
	waiter   *approvalWaiter
}

var approvalRegistry = struct {
	sync.Mutex
	entries map[string]*registeredApproval
}{entries: map[string]*registeredApproval{}}

func newApprovalBroker(conversationID, turnID string, emitter *TurnEmitter) *approvalBroker {
	return &approvalBroker{conversationID: conversationID, turnID: turnID, emitter: emitter, pending: map[string]*approvalWaiter{}}
}

// setTurnID 在 running 占位行创建后回填；工具只会在流式阶段实际运行。
func (b *approvalBroker) setTurnID(turnID string) {
	b.mu.Lock()
	b.turnID = turnID
	b.mu.Unlock()
}

// newApprovalID 生成随机高熵审批 ID；不暴露内部分配顺序。
func newApprovalID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// Await 持久化审批请求、发 pending 帧并等待裁决/取消/超时。它绝不执行工具，
// 批准后的执行由 permission.Wrapper 在 ctx 仍有效时调用原工具完成。
func (b *approvalBroker) Await(ctx context.Context, req *permission.Request) (permission.Outcome, error) {
	id, err := newApprovalID()
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	turnID := b.turnID
	b.mu.Unlock()
	if turnID == "" {
		return "", errors.New("approval broker is not bound to a turn")
	}
	req.ApprovalID = id
	req.ConversationID = b.conversationID
	req.TurnID = turnID
	if req.Effect == "" {
		req.Effect = permission.EffectUnknown
	}
	// 审计行先于任何裁决入口写入；持久化失败时工具不执行。
	if _, err := db.EntClient.KaguyaApproval.Create().
		SetApprovalID(id).SetConversationID(b.conversationID).SetTurnID(turnID).
		SetToolCallID(req.ToolCallID).SetInputHash(req.InputHash).SetToolIdentity(req.ToolIdentity).
		SetEffect(kaguyaapproval.Effect(req.Effect)).SetMode(kaguyaapproval.Mode(req.Mode)).
		SetStatus(kaguyaapproval.StatusPending).SetRequestedAt(time.Now().UTC()).Save(ctx); err != nil {
		global.Logger.Sugar().Errorf("persist approval request failed: conversation_id=%s err=%v", b.conversationID, err)
		return "", fmt.Errorf("persist approval request: %w", err)
	}

	waiter := &approvalWaiter{outcome: make(chan permission.Outcome, 1)}
	entry := &registeredApproval{
		broker: b, waiter: waiter,
		approval: dtochat.Approval{
			ApprovalID: id, ToolCallID: req.ToolCallID, ToolName: req.ToolName, ToolIdentity: req.ToolIdentity,
			Effect: string(req.Effect), Mode: string(req.Mode), Status: string(kaguyaapproval.StatusPending),
			Input: req.Input, ExpiresAt: time.Now().Add(approvalTimeout).Unix(),
		},
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		b.settleRow(id, permission.OutcomeCanceled, "turn_finished")
		return permission.OutcomeCanceled, nil
	}
	b.pending[id] = waiter
	b.mu.Unlock()
	approvalRegistry.Lock()
	approvalRegistry.entries[id] = entry
	approvalRegistry.Unlock()
	defer func() {
		approvalRegistry.Lock()
		if approvalRegistry.entries[id] == entry {
			delete(approvalRegistry.entries, id)
		}
		approvalRegistry.Unlock()
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()

	b.emit(entry.approval)
	timer := time.NewTimer(approvalTimeout)
	defer timer.Stop()
	select {
	case outcome := <-waiter.outcome:
		final := b.consume(ctx, id, outcome)
		entry.approval.Status = string(approvalStatus(final))
		b.emit(entry.approval)
		return final, nil
	case <-timer.C:
		final := b.settleRow(id, permission.OutcomeExpired, "")
		entry.approval.Status = string(approvalStatus(final))
		b.emit(entry.approval)
		return final, nil
	case <-ctx.Done():
		final := b.settleRow(id, permission.OutcomeCanceled, "")
		entry.approval.Status = string(approvalStatus(final))
		b.emit(entry.approval)
		return final, nil
	}
}

// consume 处理从裁决接口收到的结果：批准先 CAS 为 consumed 再执行；等待期间
// ctx 已失效时不执行并把未消费的批准标记为取消。
func (b *approvalBroker) consume(ctx context.Context, id string, received permission.Outcome) permission.Outcome {
	switch received {
	case permission.OutcomeApproved:
		if ctx.Err() != nil {
			return b.settleRow(id, permission.OutcomeCanceled, "context_done")
		}
		if ok, err := transitionApproval(ctx, id, []kaguyaapproval.Status{kaguyaapproval.StatusApproved}, kaguyaapproval.StatusConsumed, "", ""); err == nil && ok {
			return permission.OutcomeApproved
		}
		return b.currentOutcome(ctx, id)
	case permission.OutcomeRejected:
		return permission.OutcomeRejected
	default:
		return b.settleRow(id, received, "")
	}
}

// settleRow 在超时/取消/关停时终结未消费的审批；若裁决已抢先批准则改为取消，
// 只有新决议确实已拒绝时才回报拒绝。返回行内当前真实结果。
func (b *approvalBroker) settleRow(id string, preferred permission.Outcome, reason string) permission.Outcome {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status := kaguyaapproval.StatusCanceled
	if preferred == permission.OutcomeExpired {
		status = kaguyaapproval.StatusExpired
	}
	if ok, err := transitionApproval(ctx, id, []kaguyaapproval.Status{kaguyaapproval.StatusPending, kaguyaapproval.StatusApproved}, status, "", reason); err == nil && ok {
		return preferred
	}
	return b.currentOutcome(ctx, id)
}

// currentOutcome 读取审计行当前状态并映射为等待结果。
func (b *approvalBroker) currentOutcome(ctx context.Context, id string) permission.Outcome {
	row, err := db.EntClient.KaguyaApproval.Query().Where(kaguyaapproval.ApprovalIDEQ(id)).Only(ctx)
	if err != nil {
		return permission.OutcomeCanceled
	}
	return outcomeFromStatus(row.Status, row.Decision)
}

func outcomeFromStatus(status kaguyaapproval.Status, decision string) permission.Outcome {
	switch status {
	case kaguyaapproval.StatusApproved, kaguyaapproval.StatusConsumed:
		return permission.OutcomeApproved
	case kaguyaapproval.StatusRejected:
		return permission.OutcomeRejected
	case kaguyaapproval.StatusExpired:
		return permission.OutcomeExpired
	default:
		return permission.OutcomeCanceled
	}
}

func approvalStatus(outcome permission.Outcome) kaguyaapproval.Status {
	switch outcome {
	case permission.OutcomeApproved:
		return kaguyaapproval.StatusApproved
	case permission.OutcomeRejected:
		return kaguyaapproval.StatusRejected
	case permission.OutcomeExpired:
		return kaguyaapproval.StatusExpired
	default:
		return kaguyaapproval.StatusCanceled
	}
}

// emit 非阻塞发送审批状态帧；连接已断开时忽略，不影响审批持久化。
func (b *approvalBroker) emit(approval dtochat.Approval) {
	if b.emitter == nil {
		return
	}
	payload := approval
	b.emitter.Emit(&dtochat.ChatResp{
		Chat:     dtochat.Chat{ID: b.conversationID, Flag: dtochat.ChatFlagApproval},
		Approval: &payload,
	})
}

// Close 取消该轮全部未完成审批；Chat 在所有出口调用，保证不会跨轮残留 waiter。
func (b *approvalBroker) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	waiters := make([]*approvalWaiter, 0, len(b.pending))
	for _, waiter := range b.pending {
		waiters = append(waiters, waiter)
	}
	b.mu.Unlock()
	for _, waiter := range waiters {
		select {
		case waiter.outcome <- permission.OutcomeCanceled:
		default:
		}
	}
}

// transitionApproval 对指定状态集合执行一次 CAS 更新；返回是否更新成功。
func transitionApproval(ctx context.Context, id string, from []kaguyaapproval.Status, to kaguyaapproval.Status, decision, reason string) (bool, error) {
	update := db.EntClient.KaguyaApproval.Update().
		Where(kaguyaapproval.ApprovalIDEQ(id), kaguyaapproval.StatusIn(from...)).
		SetStatus(to).SetResolvedAt(time.Now().UTC())
	if decision != "" {
		update.SetDecision(decision)
	}
	if reason != "" {
		update.SetReasonCode(reason)
	}
	if to == kaguyaapproval.StatusConsumed {
		update.SetConsumedAt(time.Now().UTC())
	}
	changed, err := update.Save(ctx)
	return changed > 0, err
}

// DecideApproval 处理一次用户裁决：核对会话归属、以 CAS 保证只生效一次；
// 相同裁决重复请求幂等返回，冲突裁决返回 ApprovalConflict。
func (s *AgentSvc) DecideApproval(ctx context.Context, conversationID, approvalID, decision string) (*dtochat.ApprovalDecisionResp, error) {
	row, err := db.EntClient.KaguyaApproval.Query().Where(kaguyaapproval.ApprovalIDEQ(approvalID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrApprovalNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.ConversationID != conversationID {
		return nil, ErrApprovalNotFound
	}
	// 等待中的审批才可通过接口裁决；优先走内存注册表。
	approvalRegistry.Lock()
	entry := approvalRegistry.entries[approvalID]
	approvalRegistry.Unlock()
	if row.Status == kaguyaapproval.StatusPending {
		if entry == nil {
			// 当前进程没有等待者（崩溃遗留或旧实例），不可执行。
			return nil, ErrApprovalNotFound
		}
		target := kaguyaapproval.StatusRejected
		if decision == "approve" {
			target = kaguyaapproval.StatusApproved
		}
		ok, err := transitionApproval(ctx, approvalID, []kaguyaapproval.Status{kaguyaapproval.StatusPending}, target, decision, "")
		if err != nil {
			return nil, err
		}
		if ok {
			outcome := permission.OutcomeRejected
			if decision == "approve" {
				outcome = permission.OutcomeApproved
			}
			select {
			case entry.waiter.outcome <- outcome:
			default:
			}
			return &dtochat.ApprovalDecisionResp{ApprovalID: approvalID, Status: string(target), Decision: decision}, nil
		}
		// CAS 失败：并发裁决已生效，回读最终状态。
		row, err = db.EntClient.KaguyaApproval.Query().Where(kaguyaapproval.ApprovalIDEQ(approvalID)).Only(ctx)
		if err != nil {
			return nil, err
		}
	}
	switch row.Status {
	case kaguyaapproval.StatusApproved, kaguyaapproval.StatusRejected, kaguyaapproval.StatusConsumed:
		if row.Decision == decision {
			return &dtochat.ApprovalDecisionResp{ApprovalID: approvalID, Status: string(row.Status), Decision: decision}, nil
		}
		return nil, ErrApprovalConflict
	default:
		// expired/canceled：不允许再裁决，也不能复用为执行授权。
		return nil, ErrApprovalConflict
	}
}

// ConversationApprovals 返回会话当前仍等待裁决的审批，用于页面刷新后的状态补齐。
func (s *AgentSvc) ConversationApprovals(ctx context.Context, conversationID string) ([]dtochat.Approval, error) {
	exists, err := db.EntClient.KaguyaConversation.Query().
		Where(kaguyaconversation.IDEQ(conversationID), kaguyaconversation.DeletedAtIsNil()).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrConversationNotFound
	}
	rows, err := db.EntClient.KaguyaApproval.Query().
		Where(kaguyaapproval.ConversationIDEQ(conversationID), kaguyaapproval.StatusEQ(kaguyaapproval.StatusPending)).
		Order(ent.Asc(kaguyaapproval.FieldRequestedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]dtochat.Approval, 0, len(rows))
	for _, row := range rows {
		items = append(items, dtochat.Approval{
			ApprovalID: row.ApprovalID, ToolCallID: row.ToolCallID, ToolIdentity: row.ToolIdentity,
			Effect: string(row.Effect), Mode: string(row.Mode), Status: string(row.Status),
		})
	}
	return items, nil
}

// ReconcileApprovals 在启动时终结遗留的 pending/approved 未消费审批；
// 崩溃中断的批准绝不重放执行。
func ReconcileApprovals(ctx context.Context) error {
	_, err := db.EntClient.KaguyaApproval.Update().
		Where(kaguyaapproval.StatusIn(kaguyaapproval.StatusPending, kaguyaapproval.StatusApproved)).
		SetStatus(kaguyaapproval.StatusCanceled).SetReasonCode("startup_recovery").SetResolvedAt(time.Now().UTC()).Save(ctx)
	return err
}
