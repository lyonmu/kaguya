package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
	"github.com/lyonmu/kaguya/internal/ent/predicate"
	"go.uber.org/zap"

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
	ErrApprovalConflict        = errors.New("approval already resolved with a different decision")
	ErrApprovalDecisionInvalid = errors.New("invalid approval decision")
)

// approvalBroker 是单轮对话的审批等待器，实现 permission.Approver。
// 每个 pending 都先持久化为审计行再注册裁决入口，等待期间不持有任何工具、
// 文件或 MCP 连接锁。
type approvalBroker struct {
	conversationID string
	turnID         string
	emitter        *TurnEmitter
	client         *ent.Client

	mu      sync.Mutex
	closed  bool
	pending map[string]*approvalWaiter
}

type approvalWaiter struct {
	outcome chan permission.Outcome
}

// registeredApproval 是裁决接口访问当前进程等待中的审批的入口。
type registeredApproval struct {
	broker    *approvalBroker
	approval  dtochat.Approval
	waiter    *approvalWaiter
	request   permission.Request
	expiresAt time.Time
}

var approvalRegistry = struct {
	sync.Mutex
	entries map[string]*registeredApproval
}{entries: map[string]*registeredApproval{}}

func newApprovalBroker(conversationID, turnID string, emitter *TurnEmitter) *approvalBroker {
	return &approvalBroker{conversationID: conversationID, turnID: turnID, emitter: emitter, client: db.EntClient, pending: map[string]*approvalWaiter{}}
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
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if req == nil || !req.Mode.Valid() || req.ToolCallID == "" || req.ToolIdentity == "" || req.InputHash != permission.HashInput(req.Input) {
		return "", ErrApprovalConflict
	}
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
	requestedAt := time.Now().UTC()
	expiresAt := requestedAt.Add(approvalTimeout)
	entry := &registeredApproval{
		broker: b, waiter: &approvalWaiter{outcome: make(chan permission.Outcome, 1)},
		request: *req, expiresAt: expiresAt,
		approval: dtochat.Approval{
			ApprovalID: id, ToolCallID: req.ToolCallID, ToolName: req.ToolName, ToolIdentity: req.ToolIdentity,
			Effect: string(req.Effect), Mode: string(req.Mode), Status: string(kaguyaapproval.StatusPending),
			Input: approvalDisplayInput(req), ExpiresAt: expiresAt.Unix(), ExecutionState: string(kaguyaapproval.ExecutionStateNotStarted),
		},
	}
	// 先验证活会话/轮次，再持久化请求；事务完成前没有可裁决入口。
	tx, err := b.client.Tx(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateApprovalTurn(ctx, tx.Client(), entry.request); err != nil {
		return "", err
	}
	if _, err := tx.KaguyaApproval.Create().
		SetApprovalID(id).SetConversationID(b.conversationID).SetTurnID(turnID).
		SetToolCallID(req.ToolCallID).SetInputHash(req.InputHash).SetToolIdentity(req.ToolIdentity).
		SetEffect(kaguyaapproval.Effect(req.Effect)).SetMode(kaguyaapproval.Mode(req.Mode)).
		SetStatus(kaguyaapproval.StatusPending).SetRequestedAt(requestedAt).SetExpiresAt(expiresAt).Save(ctx); err != nil {
		return "", fmt.Errorf("persist approval request: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit approval request: %w", err)
	}
	waiter := entry.waiter
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		b.settleRow(id, permission.OutcomeCanceled, "turn_finished")
		return permission.OutcomeCanceled, nil
	}
	b.pending[id] = waiter
	approvalRegistry.Lock()
	approvalRegistry.entries[id] = entry
	approvalRegistry.Unlock()
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		approvalRegistry.Lock()
		if approvalRegistry.entries[id] == entry {
			delete(approvalRegistry.entries, id)
		}
		approvalRegistry.Unlock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()

	b.emit(entry.approval)
	timer := time.NewTimer(time.Until(expiresAt))
	defer timer.Stop()
	select {
	case outcome := <-waiter.outcome:
		final, err := b.consume(ctx, entry, outcome)
		b.emitCurrent(ctx, entry.approval)
		return final, err
	case <-timer.C:
		final := b.settleRow(id, permission.OutcomeExpired, "")
		b.emitCurrent(context.Background(), entry.approval)
		return final, nil
	case <-ctx.Done():
		final := b.settleRow(id, permission.OutcomeCanceled, "")
		b.emitCurrent(context.Background(), entry.approval)
		return final, ctx.Err()
	}
}

// consume 处理从裁决接口收到的结果：批准先 CAS 为 consumed 再执行；等待期间
// ctx 已失效时不执行并把未消费的批准标记为取消。
func (b *approvalBroker) consume(ctx context.Context, entry *registeredApproval, received permission.Outcome) (permission.Outcome, error) {
	id := entry.request.ApprovalID
	if received != permission.OutcomeApproved {
		if received == permission.OutcomeRejected {
			return received, nil
		}
		return b.settleRow(id, received, ""), nil
	}
	if err := ctx.Err(); err != nil {
		return b.settleRow(id, permission.OutcomeCanceled, "context_done"), err
	}
	if !entry.active() {
		return b.settleRow(id, permission.OutcomeCanceled, "turn_finished"), nil
	}
	err := b.consumeApproved(ctx, entry)
	if err != nil {
		b.settleRow(id, permission.OutcomeCanceled, "consume_failed")
		return "", err
	}
	// Save 的时间谓词可能在等待事务提交前构造。提交成功也不能延长授权期限。
	if !time.Now().Before(entry.expiresAt) {
		b.RecordExecution(ctx, &entry.request, fantasy.ToolResponse{}, context.DeadlineExceeded, false)
		return permission.OutcomeExpired, nil
	}
	// 取得消费权后若轮次入口已关闭，也不能执行；持久 unknown 如实保留。
	if !entry.active() {
		return permission.OutcomeCanceled, nil
	}
	return permission.OutcomeApproved, nil
}

func (entry *registeredApproval) active() bool {
	b := entry.broker
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.turnID != entry.request.TurnID {
		return false
	}
	approvalRegistry.Lock()
	defer approvalRegistry.Unlock()
	return approvalRegistry.entries[entry.request.ApprovalID] == entry
}

// consumeApproved 只有本次完整事务提交成功才返回 nil；状态回读不能授予执行权。
func (b *approvalBroker) consumeApproved(ctx context.Context, entry *registeredApproval) error {
	tx, err := b.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin approval consumption: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	row, err := tx.KaguyaApproval.Query().Where(kaguyaapproval.ApprovalIDEQ(entry.request.ApprovalID)).Only(ctx)
	if err != nil {
		return err
	}
	if err := validateApprovalBinding(row, entry); err != nil {
		return err
	}
	if err := validateApprovalTurn(ctx, tx.Client(), entry.request); err != nil {
		return err
	}
	changed, err := tx.KaguyaApproval.Update().Where(approvalBindingPredicates(entry.request)...).
		Where(kaguyaapproval.StatusEQ(kaguyaapproval.StatusApproved), kaguyaapproval.DecisionEQ("approve"), kaguyaapproval.ExpiresAtGT(time.Now().UTC())).
		SetStatus(kaguyaapproval.StatusConsumed).SetConsumedAt(time.Now().UTC()).SetExecutionState(kaguyaapproval.ExecutionStateUnknown).Save(ctx)
	if err != nil {
		return fmt.Errorf("consume approval: %w", err)
	}
	if changed != 1 {
		return ErrApprovalConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit approval consumption: %w", err)
	}
	return nil
}

func approvalBindingPredicates(req permission.Request) []predicate.KaguyaApproval {
	return []predicate.KaguyaApproval{
		kaguyaapproval.ApprovalIDEQ(req.ApprovalID), kaguyaapproval.ConversationIDEQ(req.ConversationID),
		kaguyaapproval.TurnIDEQ(req.TurnID), kaguyaapproval.ToolCallIDEQ(req.ToolCallID),
		kaguyaapproval.InputHashEQ(req.InputHash), kaguyaapproval.ToolIdentityEQ(req.ToolIdentity),
		kaguyaapproval.ModeEQ(kaguyaapproval.Mode(req.Mode)), kaguyaapproval.EffectEQ(kaguyaapproval.Effect(req.Effect)),
		kaguyaapproval.DeletedAtIsNil(),
	}
}

func validateApprovalBinding(row *ent.KaguyaApproval, entry *registeredApproval) error {
	req := entry.request
	if row.DeletedAt != nil || row.ApprovalID != req.ApprovalID || row.ConversationID != req.ConversationID ||
		row.TurnID != req.TurnID || row.ToolCallID != req.ToolCallID || row.InputHash != req.InputHash ||
		row.ToolIdentity != req.ToolIdentity || string(row.Mode) != string(req.Mode) || string(row.Effect) != string(req.Effect) ||
		req.InputHash != permission.HashInput(req.Input) || row.ExpiresAt == nil || !row.ExpiresAt.Equal(entry.expiresAt) || !time.Now().Before(*row.ExpiresAt) {
		return ErrApprovalConflict
	}
	return nil
}

func validateApprovalTurn(ctx context.Context, client *ent.Client, req permission.Request) error {
	conversation, err := client.KaguyaConversation.Get(ctx, req.ConversationID)
	if ent.IsNotFound(err) || (err == nil && conversation.DeletedAt != nil) {
		return ErrConversationNotFound
	}
	if err != nil {
		return err
	}
	turn, err := client.KaguyaChatTurn.Get(ctx, req.TurnID)
	if ent.IsNotFound(err) {
		return ErrApprovalConflict
	}
	if err != nil {
		return err
	}
	if turn.DeletedAt != nil || turn.ConversationID != req.ConversationID || turn.Status != kaguyachatturn.StatusRunning ||
		string(turn.PermissionMode) != string(req.Mode) || string(conversation.PermissionMode) != string(req.Mode) {
		return ErrApprovalConflict
	}
	return nil
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
	if ok, err := transitionApproval(ctx, b.client, id, []kaguyaapproval.Status{kaguyaapproval.StatusPending, kaguyaapproval.StatusApproved}, status, "", reason); err == nil && ok {
		return preferred
	}
	return b.currentOutcome(ctx, id)
}

// currentOutcome 读取审计行当前状态并映射为等待结果。
func (b *approvalBroker) currentOutcome(ctx context.Context, id string) permission.Outcome {
	row, err := b.client.KaguyaApproval.Query().Where(kaguyaapproval.ApprovalIDEQ(id)).Only(ctx)
	if err != nil {
		return permission.OutcomeCanceled
	}
	return outcomeFromStatus(row.Status)
}

func outcomeFromStatus(status kaguyaapproval.Status) permission.Outcome {
	switch status {
	case kaguyaapproval.StatusRejected:
		return permission.OutcomeRejected
	case kaguyaapproval.StatusExpired:
		return permission.OutcomeExpired
	default:
		return permission.OutcomeCanceled
	}
}

// emitCurrent 按实际持久状态发送快照，不能把 consumed 伪报 approved。
func (b *approvalBroker) emitCurrent(ctx context.Context, approval dtochat.Approval) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	row, err := b.client.KaguyaApproval.Query().Where(kaguyaapproval.ApprovalIDEQ(approval.ApprovalID)).Only(queryCtx)
	if err != nil {
		return
	}
	display := approvalAudit(row)
	display.ToolName, display.Input = approval.ToolName, approval.Input
	b.emit(display)
}

// emit 经 TurnEmitter 串行发送，阻塞时响应轮次 ctx，不持 registry 或工具锁。
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
	approvalRegistry.Lock()
	for id, waiter := range b.pending {
		if entry := approvalRegistry.entries[id]; entry != nil && entry.broker == b {
			delete(approvalRegistry.entries, id)
		}
		waiters = append(waiters, waiter)
	}
	approvalRegistry.Unlock()
	b.mu.Unlock()
	for _, waiter := range waiters {
		select {
		case waiter.outcome <- permission.OutcomeCanceled:
		default:
		}
	}
}

// transitionApproval 对指定状态集合执行一次 CAS 更新；返回是否更新成功。
func transitionApproval(ctx context.Context, client *ent.Client, id string, from []kaguyaapproval.Status, to kaguyaapproval.Status, decision, reason string) (bool, error) {
	update := client.KaguyaApproval.Update().
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
	if decision != "approve" && decision != "reject" {
		return nil, ErrApprovalDecisionInvalid
	}
	row, err := db.EntClient.KaguyaApproval.Query().Where(kaguyaapproval.ApprovalIDEQ(approvalID), kaguyaapproval.DeletedAtIsNil()).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrApprovalNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.ConversationID != conversationID {
		return nil, ErrApprovalNotFound
	}
	exists, err := db.EntClient.KaguyaConversation.Query().Where(kaguyaconversation.IDEQ(conversationID), kaguyaconversation.DeletedAtIsNil()).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrConversationNotFound
	}
	if row.Status != kaguyaapproval.StatusPending {
		return approvalDecisionResult(row, decision)
	}
	approvalRegistry.Lock()
	entry := approvalRegistry.entries[approvalID]
	approvalRegistry.Unlock()
	if entry == nil || !entry.active() {
		return nil, ErrApprovalNotFound
	}
	tx, err := db.EntClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row, err = tx.KaguyaApproval.Query().Where(kaguyaapproval.ApprovalIDEQ(approvalID)).Only(ctx)
	if err != nil {
		return nil, err
	}
	if row.Status != kaguyaapproval.StatusPending {
		return approvalDecisionResult(row, decision)
	}
	if err := validateApprovalBinding(row, entry); err != nil {
		return nil, err
	}
	if err := validateApprovalTurn(ctx, tx.Client(), entry.request); err != nil {
		return nil, err
	}
	if !entry.active() {
		return nil, ErrApprovalNotFound
	}
	target, outcome := kaguyaapproval.StatusRejected, permission.OutcomeRejected
	if decision == "approve" {
		target, outcome = kaguyaapproval.StatusApproved, permission.OutcomeApproved
	}
	changed, err := tx.KaguyaApproval.Update().Where(approvalBindingPredicates(entry.request)...).
		Where(kaguyaapproval.StatusEQ(kaguyaapproval.StatusPending), kaguyaapproval.ExpiresAtGT(time.Now().UTC())).
		SetStatus(target).SetDecision(decision).SetResolvedAt(time.Now().UTC()).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, ErrApprovalConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	select {
	case entry.waiter.outcome <- outcome:
	default:
	}
	return &dtochat.ApprovalDecisionResp{ApprovalID: approvalID, Status: string(target), Decision: decision}, nil
}

func approvalDecisionResult(row *ent.KaguyaApproval, decision string) (*dtochat.ApprovalDecisionResp, error) {
	switch row.Status {
	case kaguyaapproval.StatusApproved, kaguyaapproval.StatusRejected, kaguyaapproval.StatusConsumed:
		if row.Decision == decision {
			return &dtochat.ApprovalDecisionResp{ApprovalID: row.ApprovalID, Status: string(row.Status), Decision: decision}, nil
		}
	}
	return nil, ErrApprovalConflict
}

// RecordExecution 在工具返回后独立记录结果；失败时保留 unknown，不重放工具。
func (b *approvalBroker) RecordExecution(ctx context.Context, req *permission.Request, response fantasy.ToolResponse, runErr error, started bool) {
	state, reason := kaguyaapproval.ExecutionStateSucceeded, ""
	switch {
	case ctx.Err() != nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded):
		state, reason = kaguyaapproval.ExecutionStateCanceled, "execution_canceled"
		if !started {
			reason = "canceled_before_execution"
		}
	case runErr != nil || response.IsError:
		state, reason = kaguyaapproval.ExecutionStateFailed, "tool_failed"
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	changed, err := b.client.KaguyaApproval.Update().Where(approvalBindingPredicates(*req)...).
		Where(kaguyaapproval.StatusEQ(kaguyaapproval.StatusConsumed), kaguyaapproval.ExecutionStateEQ(kaguyaapproval.ExecutionStateUnknown), kaguyaapproval.ExecutionFinishedAtIsNil()).
		SetExecutionState(state).SetExecutionReasonCode(reason).SetExecutionFinishedAt(time.Now().UTC()).Save(cleanupCtx)
	if err != nil || changed != 1 {
		if global.Logger != nil {
			global.Logger.Warn("persist approval execution result failed; result remains unknown", zap.String("approval_id", req.ApprovalID), zap.String("reason", "execution_result_persist_failed"))
		}
		return
	}
	b.emitCurrent(cleanupCtx, dtochat.Approval{ApprovalID: req.ApprovalID, ToolName: req.ToolName})
}

// approvalAudit 只返回关联元信息；旧 consumed 无结果时间时不能用默认值伪造未执行。
func approvalAudit(row *ent.KaguyaApproval) dtochat.Approval {
	state := string(row.ExecutionState)
	if row.Status == kaguyaapproval.StatusConsumed && row.ExecutionFinishedAt == nil {
		state = string(kaguyaapproval.ExecutionStateUnknown)
	}
	toolName := ""
	if strings.HasPrefix(row.ToolIdentity, "builtin:") || strings.HasPrefix(row.ToolIdentity, "memory:") {
		_, toolName, _ = strings.Cut(row.ToolIdentity, ":")
	}
	result := dtochat.Approval{ApprovalID: row.ApprovalID, ToolCallID: row.ToolCallID, ToolName: toolName, ToolIdentity: row.ToolIdentity,
		Effect: string(row.Effect), Mode: string(row.Mode), Status: string(row.Status), ExecutionState: state, ExecutionReasonCode: row.ExecutionReasonCode}
	if row.ExpiresAt != nil {
		result.ExpiresAt = row.ExpiresAt.Unix()
	}
	if row.ExecutionFinishedAt != nil {
		result.ExecutionFinishedAt = row.ExecutionFinishedAt.Unix()
	}
	return result
}

// approvalDisplayInput 仅脱敏展示副本；执行和哈希始终使用原始不可变参数。
// 与既有秘密规则使用相同字段词汇；不能识别任意自定义字段中的秘密。
func approvalDisplayInput(req *permission.Request) string {
	if !strings.HasPrefix(req.ToolIdentity, "mcp:") {
		return req.Input
	}
	var input any
	if err := json.Unmarshal([]byte(req.Input), &input); err != nil {
		return "[Parameters unavailable; input hash: " + req.InputHash + "]"
	}
	var redact func(any)
	redact = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(key))
				if normalized == "authorization" || normalized == "apikey" || normalized == "passwd" || normalized == "pwd" ||
					strings.HasSuffix(normalized, "token") || strings.HasSuffix(normalized, "password") || strings.HasSuffix(normalized, "secret") ||
					strings.HasSuffix(normalized, "credential") || strings.HasSuffix(normalized, "credentials") || normalized == "privatekey" {
					value[key] = "[REDACTED]"
				} else {
					redact(child)
				}
			}
		case []any:
			for _, child := range value {
				redact(child)
			}
		}
	}
	redact(input)
	data, err := json.Marshal(input)
	if err != nil {
		return "[Parameters unavailable; input hash: " + req.InputHash + "]"
	}
	return string(data)
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
		approvalRegistry.Lock()
		entry := approvalRegistry.entries[row.ApprovalID]
		approvalRegistry.Unlock()
		if entry == nil || !entry.active() || validateApprovalBinding(row, entry) != nil {
			continue
		}
		if err := validateApprovalTurn(ctx, db.EntClient, entry.request); err != nil {
			if errors.Is(err, ErrConversationNotFound) || errors.Is(err, ErrApprovalConflict) {
				continue
			}
			return nil, err
		}
		display := approvalAudit(row)
		display.ToolName, display.Input = entry.approval.ToolName, entry.approval.Input
		items = append(items, display)
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
