//go:build darwin || linux

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/agent/permission"
	agentruntime "github.com/lyonmu/kaguya/internal/agent/runtime"
	"github.com/lyonmu/kaguya/internal/consts"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaapproval"
)

var approvalLongWait = flag.Bool("approval-long-wait", false, "measure the real ten-minute SQLCipher approval deadline with five waiting tools and a queued sixth tool")

type approvalLongWaitResult struct {
	ElapsedMS             float64 `json:"elapsed_ms"`
	Checks                int     `json:"pending_checks"`
	Registered            int     `json:"registry_after_cleanup"`
	Before, Peak, AfterGC chatResourceSample
}

// 默认回归校验同一实链的调度和收尾，不能代替显式十分钟资源验收。
func TestParallelApprovalExpiryThenQueuedCancellation(t *testing.T) {
	previous := approvalTimeout
	approvalTimeout = 100 * time.Millisecond
	t.Cleanup(func() { approvalTimeout = previous })
	runApprovalExpiryThenQueuedCancellation(t, time.Millisecond, nil)
}

func TestApprovalLongWaitResources(t *testing.T) {
	if !*approvalLongWait {
		t.Skip("run with -approval-long-wait -sqlcipher -timeout=13m for the actual ten-minute wait")
	}
	if !*sqlcipherTests || approvalTimeout != 10*time.Minute {
		t.Fatal("long wait requires native SQLCipher and the unchanged ten-minute production deadline")
	}
	runtime.GC()
	result := approvalLongWaitResult{Before: sampleChatResources(t)}
	result.Peak = result.Before
	ran := false
	passed := t.Run("five_expire_sixth_canceled", func(t *testing.T) {
		ran = true
		runApprovalExpiryThenQueuedCancellation(t, 5*time.Second, &result)
	})
	if !ran {
		t.Skip("long-wait workload was filtered out")
	}
	runtime.GC()
	result.AfterGC = sampleChatResources(t)
	approvalRegistry.Lock()
	result.Registered = len(approvalRegistry.entries)
	approvalRegistry.Unlock()
	if !passed || result.Registered != 0 || PendingWork() != 0 || result.AfterGC.FDs != result.Before.FDs {
		t.Errorf("long approval owners did not settle: pass=%t registry=%d work=%d FD=%d/%d", passed, result.Registered, PendingWork(), result.AfterGC.FDs, result.Before.FDs)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("APPROVAL_LONG_WAIT_RESOURCE %s", encoded)
}

// 使用真实 Fantasy HTTP adapter、五并行槽、权限 wrapper、broker 及 SQLCipher。
// 不批准任何调用：前五个自然过期后取消新登记的第六个；生产时钟不被替换。
func runApprovalExpiryThenQueuedCancellation(t *testing.T, interval time.Duration, result *approvalLongWaitResult) {
	t.Helper()
	ctx, client := setupChatTestWithTimeout(t, approvalTimeout+2*time.Minute)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	seedApprovalTurn(t, ctx, client)
	frames := make(chan *dtochat.ChatResp, 64)
	broker := newApprovalBroker("conv-1", "turn-1", newTurnEmitter(ctx, frames))
	server := approvalBatchChatServer(t, make(chan string, 1), 6)
	t.Cleanup(server.Close)
	var runs atomic.Int64
	tool := fantasy.NewParallelAgentTool("write", "fixture", func(context.Context, struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		runs.Add(1)
		return fantasy.NewTextResponse("unexpected execution"), nil
	})
	wrapped := permission.WrapAll(permission.ModeFullApproval, broker, []permission.GatedTool{{Tool: tool, Class: permission.Classification{Identity: "builtin:write", Effect: permission.EffectMutating}}})
	agent, err := agentruntime.New(agentruntime.WithProvider(agentruntime.ProviderConfig{
		Protocol: consts.ProtocolOpenAIChat, BaseURL: server.URL, RequestPath: "/v1/chat/completions", APIKey: "fixture", ModelID: "test",
	}), agentruntime.WithTools(wrapped...))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var streamErr error
	go func() {
		defer close(done)
		_, streamErr = agent.Stream(ctx, fantasy.AgentStreamCall{Prompt: "fixture"})
		broker.Close()
		close(frames)
	}()
	t.Cleanup(func() {
		cancel()
		broker.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("approval stream did not join before fixture cleanup")
		}
	})
	started := time.Now()
	initial := make(map[string]string)
	expired := make(map[string]bool)
	var sixth string
	var seq int64
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for len(expired) != 5 || sixth == "" {
		select {
		case frame, ok := <-frames:
			if !ok || frame.Approval == nil || frame.Seq <= seq {
				t.Fatal("approval stream closed early or lost ordering")
			}
			seq = frame.Seq
			approval := frame.Approval
			switch approval.Status {
			case "pending":
				row, err := dbApprovalByID(ctx, approval.ApprovalID)
				if err != nil {
					t.Fatal(err)
				}
				if row.ExpiresAt == nil || row.ExpiresAt.Sub(row.RequestedAt) != approvalTimeout {
					t.Fatal("persisted deadline differs from production wait")
				}
				if len(initial) < 5 {
					initial[approval.ApprovalID] = approval.ToolCallID
					if len(initial) == 5 {
						broker.mu.Lock()
						pending := len(broker.pending)
						broker.mu.Unlock()
						if pending != 5 {
							t.Fatalf("sixth tool bypassed the five-slot scheduler: %d", pending)
						}
						t.Logf("five pending approvals registered; waiting for %s natural deadline", approvalTimeout)
					}
				} else {
					if sixth != "" || len(expired) == 0 {
						t.Fatal("sixth tool registered before a waiting slot expired")
					}
					sixth = approval.ApprovalID
				}
			case "expired":
				if _, ok := initial[approval.ApprovalID]; !ok || expired[approval.ApprovalID] {
					t.Fatal("unexpected or repeated expiry")
				}
				expired[approval.ApprovalID] = true
			default:
				t.Fatalf("unexpected undecided approval status: %s", approval.Status)
			}
		case <-ticker.C:
			if runs.Load() != 0 {
				t.Fatal("tool executed without approval")
			}
			// 数据库可继续读取，审批等待不能占用单连接事务。
			probeCtx, probeCancel := context.WithTimeout(ctx, time.Second)
			rows, queryErr := client.KaguyaApproval.Query().All(probeCtx)
			probeCancel()
			if queryErr != nil {
				t.Fatalf("database unavailable while approval is waiting: %v", queryErr)
			}
			for _, row := range rows {
				if row.ExpiresAt == nil || (time.Now().Before(*row.ExpiresAt) && row.Status != kaguyaapproval.StatusPending) {
					t.Fatal("approval resolved before its deadline")
				}
			}
			if result != nil {
				result.Checks++
				result.Peak.include(sampleChatResources(t))
				if result.Checks%12 == 0 {
					t.Logf("long approval wait: %s elapsed, tools executed=%d", time.Since(started).Round(time.Second), runs.Load())
				}
			}
		case <-ctx.Done():
			t.Fatalf("approval deadline did not settle within the fixture budget: %v", ctx.Err())
		}
	}
	if time.Since(started) < approvalTimeout {
		t.Fatal("approval deadline finished early")
	}
	cancel()
	broker.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("queued cancellation did not join the stream")
	}
	if !errors.Is(streamErr, context.Canceled) || runs.Load() != 0 {
		t.Fatalf("stream error=%v unauthorized executions=%d", streamErr, runs.Load())
	}
	rows, err := client.KaguyaApproval.Query().All(context.Background())
	if err != nil || len(rows) != 6 {
		t.Fatalf("approval audit rows=%d err=%v", len(rows), err)
	}
	for _, row := range rows {
		want := kaguyaapproval.StatusExpired
		if row.ApprovalID == sixth {
			want = kaguyaapproval.StatusCanceled
		}
		if row.Status != want || row.ConsumedAt != nil || row.ExecutionState != kaguyaapproval.ExecutionStateNotStarted {
			t.Fatalf("unexpected audit for %s: status=%s execution=%s", row.ToolCallID, row.Status, row.ExecutionState)
		}
		if _, err := (&AgentSvc{}).DecideApproval(context.Background(), "conv-1", row.ApprovalID, "approve"); !errors.Is(err, ErrApprovalConflict) {
			t.Fatalf("late approval accepted: %v", err)
		}
	}
	broker.mu.Lock()
	pending := len(broker.pending)
	broker.mu.Unlock()
	if pending != 0 {
		t.Fatal("broker retained completed waiters")
	}
	if result != nil {
		result.ElapsedMS = float64(time.Since(started)) / float64(time.Millisecond)
		result.Peak.include(sampleChatResources(t))
	}
	t.Log(fmt.Sprintf("natural expiry=%d queued cancellation=1 tool executions=%d", len(expired), runs.Load()))
}
