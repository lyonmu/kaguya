package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/agent/permission"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaapproval"
	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
)

type approvalClockResult struct {
	Response fantasy.ToolResponse
	Err      error
}

// All DB/context/timer owners are created and closed inside the clock bubble.
// No network, subprocess, background title queue or production clock hook is used.
func setupApprovalClock(t *testing.T) (context.Context, *ent.Client, context.CancelFunc) {
	t.Helper()
	if approvalTimeout != 10*time.Minute {
		t.Fatal("clock regressions must use the production ten-minute approval deadline")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := openDiskChatClient(t)
	oldClient, oldID, oldLogger := db.EntClient, global.Id, global.Logger
	gen := &chatTestID{}
	gen.value.Store(123456789012340)
	db.EntClient, global.Id, global.Logger = client, gen, zap.NewNop()
	t.Cleanup(func() { db.EntClient, global.Id, global.Logger = oldClient, oldID, oldLogger })
	seedApprovalTurn(t, ctx, client)
	return ctx, client, cancel
}

func startApprovalClockWrite(t *testing.T, ctx context.Context, broker *approvalBroker, dir string, runs *atomic.Int64) <-chan approvalClockResult {
	t.Helper()
	tool, input := executionMatrixTool(t, "write", dir, runs)
	wrapped := permission.WrapAll(permission.ModeFullApproval, broker, classifyRegisteredTools([]fantasy.AgentTool{tool}))[0]
	result := make(chan approvalClockResult, 1)
	go func() {
		response, err := wrapped.Run(ctx, fantasy.ToolCall{ID: "clock-call", Name: tool.Info().Name, Input: input})
		result <- approvalClockResult{Response: response, Err: err}
	}()
	return result
}

func requireApprovalClockDrain(t *testing.T, broker *approvalBroker) {
	t.Helper()
	synctest.Wait()
	broker.mu.Lock()
	pending := len(broker.pending)
	broker.mu.Unlock()
	approvalRegistry.Lock()
	retained := len(approvalRegistry.entries)
	approvalRegistry.Unlock()
	if pending != 0 || retained != 0 {
		t.Fatal("virtual-clock approval retained pending/registry owners")
	}
}

func requireApprovalClockMarker(t *testing.T, dir string, want bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "write-marker"))
	if want && (err != nil || string(data) != "effect") || !want && !errors.Is(err, os.ErrNotExist) {
		t.Fatal("clock approval actual write differs from authorization", err)
	}
}

func TestApprovalClockExpiryBoundary(t *testing.T) {
	for _, beforeDeadline := range []bool{true, false} {
		name := "exact_deadline"
		if beforeDeadline {
			name = "one_nanosecond_before"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, _, cancel := setupApprovalClock(t)
				broker := newApprovalBroker("conv-1", "turn-1", nil)
				t.Cleanup(func() { cancel(); broker.Close(); synctest.Wait() })
				var runs atomic.Int64
				dir := t.TempDir()
				started := time.Now()
				done := startApprovalClockWrite(t, ctx, broker, dir, &runs)
				synctest.Wait()
				id := readApprovalID(t)
				row, err := dbApprovalByID(ctx, id)
				if err != nil || row.ExpiresAt == nil || row.ExpiresAt.Sub(started) != approvalTimeout {
					t.Fatal("production deadline was not persisted against the virtual clock", err)
				}
				time.Sleep(approvalTimeout - time.Nanosecond)
				synctest.Wait()
				if runs.Load() != 0 {
					t.Fatal("tool ran while approval was still pending")
				}
				if beforeDeadline {
					if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", id, "approve"); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					result := <-done
					row, err = dbApprovalByID(ctx, id)
					if result.Err != nil || result.Response.IsError || runs.Load() != 1 || err != nil || row.Status != kaguyaapproval.StatusConsumed || row.ExecutionState != kaguyaapproval.ExecutionStateSucceeded {
						t.Fatal("valid pre-deadline approval did not execute exactly once", result.Err, err)
					}
				} else {
					time.Sleep(time.Nanosecond)
					synctest.Wait()
					if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", id, "approve"); !errors.Is(err, ErrApprovalConflict) {
						t.Fatal("approval at the exact deadline was accepted", err)
					}
					result := <-done
					row, err = dbApprovalByID(ctx, id)
					if result.Err != nil || !result.Response.IsError || runs.Load() != 0 || err != nil || row.Status != kaguyaapproval.StatusExpired || row.ExecutionState != kaguyaapproval.ExecutionStateNotStarted {
						t.Fatal("exact-deadline expiration produced dishonest execution", result.Err, err)
					}
				}
				requireApprovalClockMarker(t, dir, beforeDeadline)
				requireApprovalClockDrain(t, broker)
			})
		})
	}
}

func TestApprovalClockCommitInterleavings(t *testing.T) {
	for _, scenario := range []string{"decision_approve", "decision_reject", "consume_expired", "consume_canceled"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, client, cancel := setupApprovalClock(t)
				broker := newApprovalBroker("conv-1", "turn-1", nil)
				entered, release := make(chan struct{}), make(chan struct{})
				t.Cleanup(func() { cancel(); broker.Close(); synctest.Wait() })
				gateStatus := kaguyaapproval.StatusConsumed
				if scenario == "decision_approve" {
					gateStatus = kaguyaapproval.StatusApproved
				} else if scenario == "decision_reject" {
					gateStatus = kaguyaapproval.StatusRejected
				}
				client.KaguyaApproval.Use(func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
						status, ok := mutation.(*ent.KaguyaApprovalMutation).Status()
						if ok && status == gateStatus {
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
				var runs atomic.Int64
				dir := t.TempDir()
				started := time.Now()
				done := startApprovalClockWrite(t, ctx, broker, dir, &runs)
				synctest.Wait()
				id := readApprovalID(t)
				decision := "approve"
				if scenario == "decision_reject" {
					decision = "reject"
				}
				decided := make(chan error, 1)
				go func() {
					_, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", id, decision)
					decided <- err
				}()
				<-entered
				synctest.Wait()
				time.Sleep(approvalTimeout)
				synctest.Wait()
				if time.Since(started) != approvalTimeout || runs.Load() != 0 {
					t.Fatal("gate did not preserve the exact production deadline")
				}
				if scenario == "consume_canceled" {
					cancel()
				}
				close(release)
				synctest.Wait()
				if err := <-decided; err != nil {
					t.Fatal("initial valid decision failed", err)
				}
				result := <-done
				// Canceled invocation ctx cannot read its audit; the bubble's clock
				// still applies to this fresh context and the real SQLCipher query.
				row, err := dbApprovalByID(context.Background(), id)
				if err != nil || runs.Load() != 0 || !result.Response.IsError && scenario != "consume_canceled" {
					t.Fatal("late commit allowed an actual tool write", result.Err, err)
				}
				switch scenario {
				case "decision_approve":
					if result.Err != nil || row.Status != kaguyaapproval.StatusExpired || row.ExecutionState != kaguyaapproval.ExecutionStateNotStarted {
						t.Fatal("late approved decision did not settle to expiration")
					}
				case "decision_reject":
					if result.Err != nil || row.Status != kaguyaapproval.StatusRejected || row.ExecutionState != kaguyaapproval.ExecutionStateNotStarted {
						t.Fatal("deadline/reject race did not report its persisted rejection")
					}
				case "consume_expired":
					if result.Err != nil || row.Status != kaguyaapproval.StatusConsumed || row.ExecutionState != kaguyaapproval.ExecutionStateCanceled || row.ExecutionReasonCode != "canceled_before_execution" {
						t.Fatal("consumption commit extended the expired execution grant")
					}
				case "consume_canceled":
					if !errors.Is(result.Err, context.Canceled) || row.Status != kaguyaapproval.StatusCanceled || row.ExecutionState != kaguyaapproval.ExecutionStateNotStarted {
						t.Fatal("canceled consumption did not remain fail closed", result.Err)
					}
				}
				requireApprovalClockMarker(t, dir, false)
				requireApprovalClockDrain(t, broker)
			})
		})
	}
}

func TestApprovalClockBlockedPendingEmissionDoesNotExtendExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, _, cancel := setupApprovalClock(t)
		frames := make(chan *dtochat.ChatResp)
		broker := newApprovalBroker("conv-1", "turn-1", newTurnEmitter(ctx, frames))
		t.Cleanup(func() { cancel(); broker.Close(); synctest.Wait() })
		var runs atomic.Int64
		dir := t.TempDir()
		started := time.Now()
		done := startApprovalClockWrite(t, ctx, broker, dir, &runs)
		synctest.Wait() // Await is durably blocked in the pending frame send.
		id := readApprovalID(t)
		time.Sleep(approvalTimeout)
		synctest.Wait()
		if _, err := (&AgentSvc{}).DecideApproval(ctx, "conv-1", id, "approve"); !errors.Is(err, ErrApprovalConflict) || runs.Load() != 0 {
			t.Fatal("backpressured pending emission extended the deadline", err)
		}
		readerDone := make(chan struct{})
		go func() {
			defer close(readerDone)
			for range frames {
			}
		}()
		synctest.Wait()
		result := <-done
		close(frames)
		<-readerDone
		row, err := dbApprovalByID(ctx, id)
		if time.Since(started) != approvalTimeout || result.Err != nil || !result.Response.IsError || runs.Load() != 0 || err != nil || row.Status != kaguyaapproval.StatusExpired {
			t.Fatal("delayed timer setup failed to honor the persisted expiry", result.Err, err)
		}
		requireApprovalClockMarker(t, dir, false)
		requireApprovalClockDrain(t, broker)
	})
}
