package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"

	"github.com/lyonmu/kaguya/internal/agent/permission"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaapproval"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// seedApprovalTurn 创建真实运行轮次，审批测试不能依赖孤立审计行取得授权。
func seedApprovalTurn(t *testing.T, ctx context.Context, client *ent.Client) {
	t.Helper()
	if _, err := client.KaguyaConversation.Create().SetID("conv-1").SetTitle("审批测试").SetModelID("m").SetModelName("m").
		SetPermissionMode(kaguyaconversation.PermissionModeFullApproval).SetLastMessageAt(time.Now()).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.KaguyaChatTurn.Create().SetID("turn-1").SetConversationID("conv-1").SetTurnIndex(1).SetPermissionMode("full_approval").SetStatus("running").
		SetUserContent("").SetProviderID("").SetProviderName("").SetModelID("m").SetModelName("m").SetAPIProtocol("").
		SetStartedAt(time.Now()).SetFinishedAt(time.Now()).SetDurationMs(0).SetToolCalls(0).SetFinishReason("").
		SetInputTokens(0).SetOutputTokens(0).SetTotalTokens(0).SetCachedTokens(0).SetReasoningTokens(0).SetMessages([]fantasy.Message{}).Save(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalConsumptionCannotExtendDeadlineWhileCommitting(t *testing.T) {
	ctx, client := setupChatTest(t)
	seedApprovalTurn(t, ctx, client)
	previous := approvalTimeout
	approvalTimeout = 200 * time.Millisecond
	t.Cleanup(func() { approvalTimeout = previous })
	entered, release := make(chan struct{}), make(chan struct{})
	client.KaguyaApproval.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if status, ok := mutation.(*ent.KaguyaApprovalMutation).Status(); ok && status == kaguyaapproval.StatusConsumed {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return next.Mutate(ctx, mutation)
		})
	})
	broker := newApprovalBroker("conv-1", "turn-1", nil)
	defer broker.Close()
	var runs atomic.Int64
	tool := fantasy.NewAgentTool("write", "", func(context.Context, struct{}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		runs.Add(1)
		return fantasy.NewTextResponse("ok"), nil
	})
	wrapped := permission.WrapAll(permission.ModeFullApproval, broker, []permission.GatedTool{{Tool: tool, Class: permission.Classification{Identity: "builtin:write", Effect: permission.EffectMutating}}})
	done := make(chan fantasy.ToolResponse, 1)
	go func() {
		response, _ := wrapped[0].Run(ctx, fantasy.ToolCall{ID: "call-1", Input: `{}`})
		done <- response
	}()
	id := readApprovalID(t)
	row, err := dbApprovalByID(ctx, id)
	if err != nil || row.ExpiresAt == nil {
		t.Fatal("expiry missing")
	}
	if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", id, "approve"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("consumption did not enter commit gate")
	}
	timer := time.NewTimer(time.Until(*row.ExpiresAt) + time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal("deadline fixture canceled")
	}
	close(release)
	select {
	case response := <-done:
		if !response.IsError || runs.Load() != 0 {
			t.Fatal("late consumption commit extended execution authorization")
		}
	case <-ctx.Done():
		t.Fatal("expired consumption did not finish")
	}
	row, err = dbApprovalByID(ctx, id)
	if err != nil || row.Status != kaguyaapproval.StatusConsumed || row.ExecutionState != kaguyaapproval.ExecutionStateCanceled || row.ExecutionReasonCode != "canceled_before_execution" {
		t.Fatalf("late commit audit is dishonest: err=%v", err)
	}
}

func TestApprovalExecutionFailureDoesNotLogSecretParameters(t *testing.T) {
	ctx, client := setupChatTest(t)
	seedApprovalTurn(t, ctx, client)
	core, observed := observer.New(zap.DebugLevel)
	previous := global.Logger
	global.Logger = zap.New(core)
	t.Cleanup(func() { global.Logger = previous })
	const secret = "fixture-unique-secret-marker"
	input := `{"authorization":"` + secret + `","nested":{"api_key":"` + secret + `"}}`
	client.KaguyaApproval.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if _, ok := mutation.(*ent.KaguyaApprovalMutation).ExecutionFinishedAt(); ok {
				return nil, errors.New(secret)
			}
			return next.Mutate(ctx, mutation)
		})
	})
	frames := make(chan *dtochat.ChatResp, 8)
	broker := newApprovalBroker("conv-1", "turn-1", newTurnEmitter(ctx, frames))
	defer broker.Close()
	var runs atomic.Int64
	tool := fantasy.NewAgentTool("remote", "", func(_ context.Context, _ map[string]any, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
		if call.Input != input {
			return fantasy.ToolResponse{}, errors.New("execution input was replaced by display copy")
		}
		runs.Add(1)
		return fantasy.NewTextResponse("ok"), nil
	})
	wrapped := permission.WrapAll(permission.ModeFullApproval, broker, []permission.GatedTool{{Tool: tool, Class: permission.Classification{Identity: "mcp:fixture:remote:1:schema", Effect: permission.EffectUnknown}}})
	done := make(chan error, 1)
	go func() { _, err := wrapped[0].Run(ctx, fantasy.ToolCall{ID: "call-1", Input: input}); done <- err }()
	id := readApprovalID(t)
	if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", id, "approve"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil || runs.Load() != 1 {
		t.Fatalf("original tool did not execute once: %v", err)
	}
	row, err := dbApprovalByID(ctx, id)
	if err != nil || row.InputHash != permission.HashInput(input) || row.ExecutionState != kaguyaapproval.ExecutionStateUnknown {
		t.Fatal("result save failure did not preserve original hash and unknown state")
	}
	if observed.Len() == 0 {
		t.Fatal("result save failure was not observable")
	}
	for _, log := range observed.All() {
		if strings.Contains(log.Message+fmt.Sprint(log.ContextMap()), secret) {
			t.Fatal("approval result persistence logged a secret")
		}
	}
	close(frames)
	for frame := range frames {
		if frame.Approval != nil && strings.Contains(frame.Approval.Input, secret) {
			t.Fatal("approval display leaked known secret field")
		}
	}
}

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
	ctx, client := setupChatTest(t)
	seedApprovalTurn(t, ctx, client)
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
	ctx, client := setupChatTest(t)
	seedApprovalTurn(t, ctx, client)
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
	ctx, client := setupChatTest(t)
	seedApprovalTurn(t, ctx, client)
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
	seedApprovalTurn(t, ctx, client)
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
	seedApprovalTurn(t, ctx, client)
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

func TestApprovalConsumptionFailureNeverExecutesTool(t *testing.T) {
	for _, failure := range []string{"write error", "CAS lost"} {
		t.Run(failure, func(t *testing.T) {
			ctx, client := setupChatTest(t)
			seedApprovalTurn(t, ctx, client)
			client.KaguyaApproval.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					m := mutation.(*ent.KaguyaApprovalMutation)
					if status, ok := m.Status(); ok && status == kaguyaapproval.StatusConsumed {
						if failure == "write error" {
							return nil, errors.New("injected consumption persistence failure")
						}
						return 0, nil
					}
					return next.Mutate(ctx, mutation)
				})
			})
			broker := newApprovalBroker("conv-1", "turn-1", nil)
			defer broker.Close()
			var runs atomic.Int64
			tool := fantasy.NewAgentTool("write", "", func(context.Context, struct{}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
				runs.Add(1)
				return fantasy.NewTextResponse("ok"), nil
			})
			wrapped := permission.WrapAll(permission.ModeFullApproval, broker, []permission.GatedTool{{Tool: tool, Class: permission.Classification{Identity: "builtin:write", Effect: permission.EffectMutating}}})
			result := make(chan error, 1)
			go func() {
				_, err := wrapped[0].Run(ctx, fantasy.ToolCall{ID: "call-1", Name: "write", Input: `{}`})
				result <- err
			}()
			id := readApprovalID(t)
			if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", id, "approve"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if err == nil || runs.Load() != 0 {
					t.Fatalf("failed consumption executed tool: err=%v runs=%d", err, runs.Load())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("failed consumption did not finish")
			}
			row, err := dbApprovalByID(ctx, id)
			if err != nil || row.Status == kaguyaapproval.StatusConsumed {
				t.Fatalf("failed consumption misreported state: row=%+v err=%v", row, err)
			}
		})
	}
}

func TestApprovalCloseDuringPersistenceCannotRegisterWaiter(t *testing.T) {
	ctx, client := setupChatTest(t)
	seedApprovalTurn(t, ctx, client)
	started, release := make(chan struct{}), make(chan struct{})
	client.KaguyaApproval.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if mutation.Op().Is(ent.OpCreate) {
				close(started)
				<-release
			}
			return next.Mutate(ctx, mutation)
		})
	})
	broker := newApprovalBroker("conv-1", "turn-1", nil)
	result := startAwait(t, broker)
	<-started
	broker.Close()
	close(release)
	select {
	case got := <-result:
		if got.err != nil || got.outcome != permission.OutcomeCanceled {
			t.Fatalf("close during registration = %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("close lost the waiter")
	}
	approvalRegistry.Lock()
	remaining := len(approvalRegistry.entries)
	approvalRegistry.Unlock()
	if remaining != 0 {
		t.Fatalf("closed broker left %d executable registry entries", remaining)
	}
	row, err := client.KaguyaApproval.Query().Only(ctx)
	if err != nil || row.Status != kaguyaapproval.StatusCanceled {
		t.Fatalf("row=%+v err=%v", row, err)
	}
}

func TestPersistedApprovalStatusCannotGrantExecution(t *testing.T) {
	for _, status := range []kaguyaapproval.Status{kaguyaapproval.StatusApproved, kaguyaapproval.StatusConsumed} {
		if outcomeFromStatus(status) == permission.OutcomeApproved {
			t.Fatalf("stored %s granted execution without consumption", status)
		}
	}
}

func TestApprovalInvalidDecisionFailsAtServiceBoundary(t *testing.T) {
	ctx, client := setupChatTest(t)
	seedApprovalTurn(t, ctx, client)
	broker := newApprovalBroker("conv-1", "turn-1", nil)
	defer broker.Close()
	result := startAwait(t, broker)
	id := readApprovalID(t)
	if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", id, "unexpected"); !errors.Is(err, ErrApprovalDecisionInvalid) {
		t.Fatalf("invalid decision=%v", err)
	}
	row, err := dbApprovalByID(ctx, id)
	if err != nil || row.Status != kaguyaapproval.StatusPending {
		t.Fatalf("invalid decision mutated row: %+v err=%v", row, err)
	}
	broker.Close()
	<-result
}

func TestApprovalConsumptionCommitFailureNeverExecutes(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed=%t", uncertain), func(t *testing.T) {
			ctx, client, fault := setupFaultRecorderTest(t)
			seedApprovalTurn(t, ctx, client)
			client.KaguyaApproval.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					if status, ok := mutation.(*ent.KaguyaApprovalMutation).Status(); ok && status == kaguyaapproval.StatusConsumed {
						if uncertain {
							fault.failNextCommitted()
						} else {
							fault.failNext("COMMIT")
						}
					}
					return next.Mutate(ctx, mutation)
				})
			})
			broker := newApprovalBroker("conv-1", "turn-1", nil)
			defer broker.Close()
			var runs atomic.Int64
			tool := fantasy.NewAgentTool("write", "", func(context.Context, struct{}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
				runs.Add(1)
				return fantasy.NewTextResponse("ok"), nil
			})
			wrapped := permission.WrapAll(permission.ModeFullApproval, broker, []permission.GatedTool{{Tool: tool, Class: permission.Classification{Identity: "builtin:write", Effect: permission.EffectMutating}}})
			result := make(chan error, 1)
			go func() { _, err := wrapped[0].Run(ctx, fantasy.ToolCall{ID: "call-1", Input: `{}`}); result <- err }()
			id := readApprovalID(t)
			if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", id, "approve"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if !errors.Is(err, errInjectedFailure) || runs.Load() != 0 {
					t.Fatalf("commit failure granted execution: err=%v runs=%d", err, runs.Load())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("consumption did not stop after commit failure")
			}
			row, err := dbApprovalByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if uncertain && (row.Status != kaguyaapproval.StatusConsumed || approvalAudit(row).ExecutionState != "unknown") {
				t.Fatalf("uncertain commit must remain consumed/unknown: %+v", row)
			}
		})
	}
}

func TestApprovalDecisionValidatesTurnBindingAndDeadline(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(context.Context, *ent.Client, string) error
		want   error
	}{
		{"hash", func(ctx context.Context, c *ent.Client, id string) error {
			return c.KaguyaApproval.Update().Where(kaguyaapproval.ApprovalIDEQ(id)).SetInputHash("tampered").Exec(ctx)
		}, ErrApprovalConflict},
		{"identity", func(ctx context.Context, c *ent.Client, id string) error {
			return c.KaguyaApproval.Update().Where(kaguyaapproval.ApprovalIDEQ(id)).SetToolIdentity("builtin:read").Exec(ctx)
		}, ErrApprovalConflict},
		{"call", func(ctx context.Context, c *ent.Client, id string) error {
			return c.KaguyaApproval.Update().Where(kaguyaapproval.ApprovalIDEQ(id)).SetToolCallID("other-call").Exec(ctx)
		}, ErrApprovalConflict},
		{"mode", func(ctx context.Context, c *ent.Client, id string) error {
			return c.KaguyaApproval.Update().Where(kaguyaapproval.ApprovalIDEQ(id)).SetMode(kaguyaapproval.ModeFullAccess).Exec(ctx)
		}, ErrApprovalConflict},
		{"expired", func(ctx context.Context, c *ent.Client, id string) error {
			return c.KaguyaApproval.Update().Where(kaguyaapproval.ApprovalIDEQ(id)).SetExpiresAt(time.Now().Add(-time.Second)).Exec(ctx)
		}, ErrApprovalConflict},
		{"legacy deadline", func(ctx context.Context, c *ent.Client, id string) error {
			return c.KaguyaApproval.Update().Where(kaguyaapproval.ApprovalIDEQ(id)).ClearExpiresAt().Exec(ctx)
		}, ErrApprovalConflict},
		{"completed turn", func(ctx context.Context, c *ent.Client, _ string) error {
			return c.KaguyaChatTurn.UpdateOneID("turn-1").SetStatus("completed").Exec(ctx)
		}, ErrApprovalConflict},
		{"turn conversation", func(ctx context.Context, c *ent.Client, _ string) error {
			return c.KaguyaChatTurn.UpdateOneID("turn-1").SetConversationID("other").Exec(ctx)
		}, ErrApprovalConflict},
		{"conversation mode", func(ctx context.Context, c *ent.Client, _ string) error {
			return c.KaguyaConversation.UpdateOneID("conv-1").SetPermissionMode(kaguyaconversation.PermissionModeFullAccess).Exec(ctx)
		}, ErrApprovalConflict},
		{"deleted conversation", func(ctx context.Context, c *ent.Client, _ string) error {
			return c.KaguyaConversation.UpdateOneID("conv-1").SetDeletedAt(time.Now()).Exec(ctx)
		}, ErrConversationNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, client := setupChatTest(t)
			seedApprovalTurn(t, ctx, client)
			broker := newApprovalBroker("conv-1", "turn-1", nil)
			defer broker.Close()
			result := startAwait(t, broker)
			id := readApprovalID(t)
			if err := tc.mutate(ctx, client, id); err != nil {
				t.Fatal(err)
			}
			if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", id, "approve"); !errors.Is(err, tc.want) {
				t.Fatalf("decision=%v want %v", err, tc.want)
			}
			broker.Close()
			if got := <-result; got.outcome == permission.OutcomeApproved {
				t.Fatal("invalid binding granted authorization")
			}
		})
	}
}

func TestApprovalExecutionResultAndHistoryAreIndependentFromAuthorization(t *testing.T) {
	for _, state := range []string{"succeeded", "failed", "canceled", "unknown"} {
		t.Run(state, func(t *testing.T) {
			ctx, client := setupChatTest(t)
			seedApprovalTurn(t, ctx, client)
			if state == "unknown" {
				client.KaguyaApproval.Use(func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
						if _, ok := m.(*ent.KaguyaApprovalMutation).ExecutionFinishedAt(); ok {
							return nil, errors.New("execution result persistence unavailable")
						}
						return next.Mutate(ctx, m)
					})
				})
			}
			broker := newApprovalBroker("conv-1", "turn-1", nil)
			defer broker.Close()
			var runs atomic.Int64
			tool := fantasy.NewAgentTool("write", "", func(context.Context, struct{}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
				runs.Add(1)
				switch state {
				case "failed":
					return fantasy.NewTextErrorResponse("failure"), nil
				case "canceled":
					return fantasy.ToolResponse{}, context.Canceled
				default:
					return fantasy.NewTextResponse("ok"), nil
				}
			})
			wrapped := permission.WrapAll(permission.ModeFullApproval, broker, []permission.GatedTool{{Tool: tool, Class: permission.Classification{Identity: "builtin:write", Effect: permission.EffectMutating}}})
			done := make(chan struct{})
			go func() { defer close(done); _, _ = wrapped[0].Run(ctx, fantasy.ToolCall{ID: "call-1", Input: `{}`}) }()
			id := readApprovalID(t)
			items, err := (&AgentSvc{}).ConversationApprovals(ctx, "conv-1")
			if err != nil || len(items) != 1 || items[0].Input != "{}" || items[0].ExpiresAt == 0 {
				t.Fatalf("live approval snapshot=%+v err=%v", items, err)
			}
			if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", id, "approve"); err != nil {
				t.Fatal(err)
			}
			<-done
			row, err := dbApprovalByID(ctx, id)
			if err != nil || runs.Load() != 1 || row.Status != kaguyaapproval.StatusConsumed || approvalAudit(row).ExecutionState != state {
				t.Fatalf("execution=%+v err=%v runs=%d", row, err, runs.Load())
			}
			if state == "unknown" && row.ExecutionFinishedAt != nil {
				t.Fatal("failed persistence fabricated result time")
			}
			history, err := (&AgentSvc{}).ConversationTurns(ctx, "conv-1", &dtochat.TurnPageReq{Limit: 5})
			if err != nil || len(history.Items) != 1 || len(history.Items[0].Approvals) != 1 {
				t.Fatalf("history=%+v err=%v", history, err)
			}
			audit := history.Items[0].Approvals[0]
			if audit.ExecutionState != state || audit.Input != "" {
				t.Fatalf("audit leaked input or misreported result: %+v", audit)
			}
		})
	}
}

func TestApprovalDisplayRedactsOnlyCopyAndLegacyConsumedIsUnknown(t *testing.T) {
	input := `{"headers":{"Authorization":"sensitive-header"},"api_key":"sensitive-key","args":[{"password":"sensitive-password"}],"path":"safe"}`
	req := &permission.Request{ToolIdentity: "mcp:server:tool:1:schema", Input: input, InputHash: permission.HashInput(input)}
	display := approvalDisplayInput(req)
	for _, secret := range []string{"sensitive-header", "sensitive-key", "sensitive-password"} {
		if strings.Contains(display, secret) {
			t.Fatal("secret present in approval display")
		}
	}
	if req.Input != input || req.InputHash != permission.HashInput(input) || !strings.Contains(display, "safe") {
		t.Fatal("redaction altered execution input/hash or safe field")
	}
	legacy := approvalAudit(&ent.KaguyaApproval{Status: kaguyaapproval.StatusConsumed, ExecutionState: kaguyaapproval.ExecutionStateNotStarted})
	if legacy.ExecutionState != "unknown" {
		t.Fatalf("legacy consumed misreported: %+v", legacy)
	}
}
