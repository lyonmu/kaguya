package agent

import (
	"context"
	"testing"
	"time"

	"github.com/lyonmu/kaguya/internal/agent/permission"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaapproval"
)

// dbApprovalByID 读取审计行，测试断言状态转换。
func dbApprovalByID(ctx context.Context, id string) (*ent.KaguyaApproval, error) {
	return db.EntClient.KaguyaApproval.Query().Where(kaguyaapproval.ApprovalIDEQ(id)).Only(ctx)
}

func waitForApprovalEntry(t *testing.T, approvalID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		approvalRegistry.Lock()
		_, ok := approvalRegistry.entries[approvalID]
		approvalRegistry.Unlock()
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("approval %s was not registered", approvalID)
}

func startAwait(t *testing.T, broker *approvalBroker) <-chan struct {
	outcome permission.Outcome
	err     error
} {
	t.Helper()
	result := make(chan struct {
		outcome permission.Outcome
		err     error
	}, 1)
	go func() {
		outcome, err := broker.Await(context.Background(), &permission.Request{
			ToolCallID: "call-1", ToolIdentity: "builtin:write", ToolName: "write",
			Effect: permission.EffectMutating, Mode: permission.ModeFullApproval,
			Input: `{"path":"a.txt"}`, InputHash: permission.HashInput(`{"path":"a.txt"}`),
		})
		result <- struct {
			outcome permission.Outcome
			err     error
		}{outcome, err}
	}()
	return result
}

// readApprovalID 等待并读取登记表里的审批 ID（随机生成，只能回读）。
func readApprovalID(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		approvalRegistry.Lock()
		for id := range approvalRegistry.entries {
			approvalRegistry.Unlock()
			return id
		}
		approvalRegistry.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no approval registered")
	return ""
}

func TestApprovalBrokerApproveConsumesOnce(t *testing.T) {
	ctx, _ := setupChatTest(t)
	ch := make(chan *dtochat.ChatResp, 8)
	emitter := newTurnEmitter(ctx, ch)
	broker := newApprovalBroker("conv-1", "turn-1", emitter)
	defer broker.Close()
	result := startAwait(t, broker)
	id := readApprovalID(t)

	resp, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", id, "approve")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != string(kaguyaapproval.StatusApproved) {
		t.Fatalf("decision status = %q", resp.Status)
	}
	got := <-result
	if got.err != nil || got.outcome != permission.OutcomeApproved {
		t.Fatalf("await = %q, %v", got.outcome, got.err)
	}
	row, err := dbApprovalByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != kaguyaapproval.StatusConsumed || row.ConsumedAt == nil {
		t.Fatalf("approval must be consumed after execution authorization: %+v", row)
	}
	// 相同裁决幂等；冲突裁决明确失败。
	same, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", id, "approve")
	if err != nil || same.Status != string(kaguyaapproval.StatusConsumed) {
		t.Fatalf("idempotent approve failed: %+v, %v", same, err)
	}
	if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", id, "reject"); err != ErrApprovalConflict {
		t.Fatalf("conflicting decision error = %v, want ErrApprovalConflict", err)
	}
}

func TestApprovalBrokerRejectDoesNotRun(t *testing.T) {
	ctx, _ := setupChatTest(t)
	ch := make(chan *dtochat.ChatResp, 8)
	broker := newApprovalBroker("conv-1", "turn-1", newTurnEmitter(ctx, ch))
	defer broker.Close()
	result := startAwait(t, broker)
	id := readApprovalID(t)
	if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", id, "reject"); err != nil {
		t.Fatal(err)
	}
	got := <-result
	if got.outcome != permission.OutcomeRejected || got.err != nil {
		t.Fatalf("await = %q, %v", got.outcome, got.err)
	}
	row, err := dbApprovalByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != kaguyaapproval.StatusRejected || row.ConsumedAt != nil || row.Decision != "reject" {
		t.Fatalf("rejected approval must not be consumed: %+v", row)
	}
}

func TestApprovalBrokerWrongConversationIsNotFound(t *testing.T) {
	ctx, _ := setupChatTest(t)
	ch := make(chan *dtochat.ChatResp, 8)
	broker := newApprovalBroker("conv-1", "turn-1", newTurnEmitter(ctx, ch))
	defer broker.Close()
	result := startAwait(t, broker)
	id := readApprovalID(t)
	if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-other", id, "approve"); err != ErrApprovalNotFound {
		t.Fatalf("cross-conversation decision error = %v", err)
	}
	// 关闭 broker 后等待者被取消，未消费审批不残留。
	broker.Close()
	if got := <-result; got.outcome != permission.OutcomeCanceled {
		t.Fatalf("await after close = %q, want canceled", got.outcome)
	}
	row, err := dbApprovalByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != kaguyaapproval.StatusCanceled {
		t.Fatalf("approval after close = %q, want canceled", row.Status)
	}
}

func TestApprovalBrokerTimeoutDoesNotExecute(t *testing.T) {
	ctx, client := setupChatTest(t)
	previous := approvalTimeout
	approvalTimeout = 30 * time.Millisecond
	t.Cleanup(func() { approvalTimeout = previous })
	ch := make(chan *dtochat.ChatResp, 8)
	broker := newApprovalBroker("conv-1", "turn-1", newTurnEmitter(ctx, ch))
	defer broker.Close()
	result := startAwait(t, broker)
	got := <-result
	if got.outcome != permission.OutcomeExpired {
		t.Fatalf("await after timeout = %q, want expired", got.outcome)
	}
	row, err := client.KaguyaApproval.Query().Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != kaguyaapproval.StatusExpired {
		t.Fatalf("approval after timeout = %q, want expired", row.Status)
	}
	// 过期后不能再裁决。
	if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", row.ApprovalID, "approve"); err != ErrApprovalConflict {
		t.Fatalf("expired approval decision error = %v, want ErrApprovalConflict", err)
	}
}

func TestReconcileApprovalsCancelsUnconsumed(t *testing.T) {
	ctx, client := setupChatTest(t)
	created, err := client.KaguyaApproval.Create().
		SetApprovalID("stale").SetConversationID("conv-1").SetTurnID("turn-1").SetToolCallID("call-1").
		SetInputHash("hash").SetToolIdentity("builtin:write").SetStatus(kaguyaapproval.StatusPending).
		SetRequestedAt(time.Now().UTC()).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := client.KaguyaApproval.Create().
		SetApprovalID("stale-approved").SetConversationID("conv-1").SetTurnID("turn-1").SetToolCallID("call-2").
		SetInputHash("hash").SetToolIdentity("builtin:write").SetStatus(kaguyaapproval.StatusApproved).
		SetRequestedAt(time.Now().UTC()).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReconcileApprovals(ctx); err != nil {
		t.Fatal(err)
	}
	for _, row := range []*struct{ id string }{{created.ID}, {approved.ID}} {
		got, err := client.KaguyaApproval.Get(ctx, row.id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != kaguyaapproval.StatusCanceled || got.ReasonCode != "startup_recovery" {
			t.Fatalf("approval %s = %q/%q, want canceled/startup_recovery", got.ID, got.Status, got.ReasonCode)
		}
	}
}
