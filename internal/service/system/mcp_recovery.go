package system

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamcpserver"
	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
)

// mcpCircuitFailureLimit 是单个连接周期内触发熔断的 Prepare 连续失败次数。
const mcpCircuitFailureLimit = 5

// mcpPolicyState 是单个 MCP 服务的重连策略状态；连接本身仍归 manager。
type mcpPolicyState struct {
	failures int
	cycle    int64
	open     bool
	nextAt   time.Time
	openedAt time.Time
}

// mcpPolicySnapshot 是策略状态的只读快照，供响应与测试使用。
type mcpPolicySnapshot struct {
	Failures    int
	Cycle       int64
	Open        bool
	NextAt      time.Time
	OpenedAt    time.Time
	PersistFail bool
	Connecting  bool
}

// MCPRecoveryCoordinator 只发布已持久化的策略。变更入口必须持有 mcpMutations
// 的同服务门；mu 仅保护短快照，不在全局锁内等待数据库或远端连接。
type MCPRecoveryCoordinator struct {
	mu            sync.Mutex
	states        map[string]*mcpPolicyState
	persistFailed map[string]bool
	connecting    map[string]bool
}

func NewMCPRecoveryCoordinator() *MCPRecoveryCoordinator {
	return &MCPRecoveryCoordinator{states: map[string]*mcpPolicyState{}, persistFailed: map[string]bool{}, connecting: map[string]bool{}}
}

var DefaultMCPRecovery = NewMCPRecoveryCoordinator()

func (c *MCPRecoveryCoordinator) Load(row *ent.KaguyaMCPServer) mcpPolicySnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.states[row.ID]; !ok {
		state := &mcpPolicyState{failures: row.ReconnectFailures, cycle: row.ReconnectCycle, open: row.ReconnectCircuitOpen}
		if row.ReconnectNextAt != nil {
			state.nextAt = *row.ReconnectNextAt
		}
		if row.ReconnectOpenedAt != nil {
			state.openedAt = *row.ReconnectOpenedAt
		}
		c.states[row.ID] = state
	}
	// 加载行不能解除本进程的存储故障锁存。
	return c.snapshotLocked(row.ID)
}

func (c *MCPRecoveryCoordinator) Snapshot(id string) (mcpPolicySnapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, exists := c.states[id]
	return c.snapshotLocked(id), exists || c.persistFailed[id]
}

func (c *MCPRecoveryCoordinator) snapshotLocked(id string) mcpPolicySnapshot {
	snapshot := mcpPolicySnapshot{PersistFail: c.persistFailed[id], Connecting: c.connecting[id]}
	if state := c.states[id]; state != nil {
		snapshot.Failures, snapshot.Cycle, snapshot.Open = state.failures, state.cycle, state.open
		snapshot.NextAt, snapshot.OpenedAt = state.nextAt, state.openedAt
	}
	return snapshot
}

func (c *MCPRecoveryCoordinator) Allowed(id string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.persistFailed[id] {
		return false
	}
	state := c.states[id]
	return state == nil || (!state.open && (state.nextAt.IsZero() || !now.Before(state.nextAt)))
}

func (c *MCPRecoveryCoordinator) setConnecting(id string, connecting bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if connecting {
		c.connecting[id] = true
	} else {
		delete(c.connecting, id)
	}
}

// confirm 在 SQL 成功后发布私有候选。失败只锁存故障，保留最后确认值；
// 仅明确的新周期允许解除锁存。所有调用者仍持有同服务变更门。
func (c *MCPRecoveryCoordinator) confirm(id string, candidate mcpPolicySnapshot, persistErr error, newCycle bool) (mcpPolicySnapshot, error) {
	c.mu.Lock()
	if persistErr != nil {
		c.persistFailed[id] = true
		snapshot := c.snapshotLocked(id)
		c.mu.Unlock()
		if global.Logger != nil {
			global.Logger.Warn("persist MCP recovery state failed; automatic reconnect stopped for this service", zap.String("service_id", id))
		}
		return snapshot, &mcpStorageError{cause: persistErr}
	}
	c.states[id] = &mcpPolicyState{failures: candidate.Failures, cycle: candidate.Cycle, open: candidate.Open, nextAt: candidate.NextAt, openedAt: candidate.OpenedAt}
	if newCycle {
		delete(c.persistFailed, id)
	}
	snapshot := c.snapshotLocked(id)
	c.mu.Unlock()
	return snapshot, nil
}

func (c *MCPRecoveryCoordinator) storageFailure(id string, cause error) error {
	current, _ := c.Snapshot(id)
	_, err := c.confirm(id, current, cause, false)
	return err
}

// RecordSuccess 的健康轮询不能解除存储故障，也不能在未确认写入前清零。
func (c *MCPRecoveryCoordinator) RecordSuccess(ctx context.Context, id string) error {
	current, exists := c.Snapshot(id)
	if current.PersistFail {
		return ErrMCPRecoveryPersistence
	}
	if exists && current.Failures == 0 && !current.Open && current.NextAt.IsZero() && current.OpenedAt.IsZero() {
		return nil
	}
	candidate := mcpPolicySnapshot{Cycle: current.Cycle}
	_, err := c.confirm(id, candidate, c.persist(ctx, id, candidate, candidate.Cycle), false)
	return err
}

func (c *MCPRecoveryCoordinator) RecordFailure(ctx context.Context, id string, base, maxDelay time.Duration, now time.Time) (mcpPolicySnapshot, error) {
	candidate, _ := c.Snapshot(id)
	if candidate.PersistFail {
		return candidate, ErrMCPRecoveryPersistence
	}
	if candidate.Open {
		return candidate, nil
	}
	candidate.Failures++
	if candidate.Failures >= mcpCircuitFailureLimit {
		candidate.Open, candidate.OpenedAt, candidate.NextAt = true, now, time.Time{}
	} else {
		candidate.NextAt = now.Add(mcpBackoffDelay(MCPRecoveryOptions{BaseDelay: base, MaxDelay: maxDelay}, candidate.Failures))
	}
	return c.confirm(id, candidate, c.persist(ctx, id, candidate, candidate.Cycle), false)
}

func (c *MCPRecoveryCoordinator) newCycle(row *ent.KaguyaMCPServer) mcpPolicySnapshot {
	// 明确操作从刚读取的持久行确定周期，兼容 COMMIT 已生效却返回错误。
	return mcpPolicySnapshot{Cycle: row.ReconnectCycle + 1}
}

func (c *MCPRecoveryCoordinator) StartManualCycle(ctx context.Context, id string) (mcpPolicySnapshot, error) {
	row, err := mcpFind(ctx, id)
	if err != nil {
		current, _ := c.Snapshot(id)
		return c.confirm(id, current, err, false)
	}
	candidate := c.newCycle(row)
	return c.confirm(id, candidate, c.persist(ctx, id, candidate, row.ReconnectCycle), true)
}

// Clear 保留周期身份；停用/删除即使清理写入失败也不能释放存储故障锁存。
func (c *MCPRecoveryCoordinator) Clear(ctx context.Context, id string) error {
	current, _ := c.Snapshot(id)
	candidate := mcpPolicySnapshot{Cycle: current.Cycle}
	_, err := c.confirm(id, candidate, c.persist(ctx, id, candidate, candidate.Cycle), false)
	return err
}

// 删除且清理确认成功后回收无调用者的缓存，避免反复创建/删除服务累积状态。
// 已锁存的故障仍保留最小标记；缺少 state 不能绕过它。
func (c *MCPRecoveryCoordinator) forgetDeleted(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.states, id)
	delete(c.connecting, id)
}

// policyUpdate 同时用于独立策略保存与配置+新周期的单次原子 UPDATE。
// 不采用 upsert；已不存在的行必须返回错误，不能假装成功或重建服务。
func policyUpdate(update *ent.KaguyaMCPServerUpdateOne, snapshot mcpPolicySnapshot) *ent.KaguyaMCPServerUpdateOne {
	update.SetReconnectFailures(snapshot.Failures).SetReconnectCircuitOpen(snapshot.Open).SetReconnectCycle(snapshot.Cycle)
	if snapshot.NextAt.IsZero() {
		update.ClearReconnectNextAt()
	} else {
		update.SetReconnectNextAt(snapshot.NextAt)
	}
	if snapshot.OpenedAt.IsZero() {
		update.ClearReconnectOpenedAt()
	} else {
		update.SetReconnectOpenedAt(snapshot.OpenedAt)
	}
	return update
}

func (c *MCPRecoveryCoordinator) persist(ctx context.Context, id string, snapshot mcpPolicySnapshot, expectedCycle int64) error {
	return policyUpdate(db.EntClient.KaguyaMCPServer.UpdateOneID(id).Where(kaguyamcpserver.ReconnectCycleEQ(expectedCycle)), snapshot).Exec(ctx)
}

// 错误链保留存储原因供 errors.Is/As 检查，展示和日志不能包含驱动原文。
type mcpStorageError struct{ cause error }

func (e *mcpStorageError) Error() string   { return ErrMCPRecoveryPersistence.Error() }
func (e *mcpStorageError) Unwrap() []error { return []error{ErrMCPRecoveryPersistence, e.cause} }

// mcpBackoffDelay 返回第 failures 次失败后的重试延迟：BaseDelay × 2^(failures-1)，上限 MaxDelay。
func mcpBackoffDelay(options MCPRecoveryOptions, failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	if failures > 32 {
		return options.MaxDelay
	}
	delay := time.Duration(float64(options.BaseDelay) * math.Pow(2, float64(failures-1)))
	if delay <= 0 || delay > options.MaxDelay {
		return options.MaxDelay
	}
	return delay
}
