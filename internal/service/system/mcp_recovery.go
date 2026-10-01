package system

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/ent"
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
}

// MCPRecoveryCoordinator 统一自动重连与手动重连的失败计数、熔断与周期。
// 状态读写都在同一实例内串行；数据库持久化在锁外执行，避免阻塞状态读取。
type MCPRecoveryCoordinator struct {
	mu            sync.Mutex
	states        map[string]*mcpPolicyState
	persistFailed map[string]bool
}

func NewMCPRecoveryCoordinator() *MCPRecoveryCoordinator {
	return &MCPRecoveryCoordinator{states: map[string]*mcpPolicyState{}, persistFailed: map[string]bool{}}
}

// DefaultMCPRecovery 是进程内默认协调器；测试可替换为独立实例。
var DefaultMCPRecovery = NewMCPRecoveryCoordinator()

// Load 从数据库行恢复策略状态；已有内存状态时不覆盖，避免用旧库值冲掉手动新周期。
func (c *MCPRecoveryCoordinator) Load(row *ent.KaguyaMCPServer) mcpPolicySnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.states[row.ID]; ok {
		return c.snapshotLocked(row.ID)
	}
	state := &mcpPolicyState{failures: row.ReconnectFailures, cycle: row.ReconnectCycle, open: row.ReconnectCircuitOpen}
	if row.ReconnectNextAt != nil {
		state.nextAt = *row.ReconnectNextAt
	}
	if row.ReconnectOpenedAt != nil {
		state.openedAt = *row.ReconnectOpenedAt
	}
	c.states[row.ID] = state
	delete(c.persistFailed, row.ID)
	return c.snapshotLocked(row.ID)
}

// Snapshot 返回内存中的策略状态；不存在时返回零值快照。
func (c *MCPRecoveryCoordinator) Snapshot(id string) (mcpPolicySnapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.states[id]; !ok {
		return mcpPolicySnapshot{}, false
	}
	return c.snapshotLocked(id), true
}

func (c *MCPRecoveryCoordinator) snapshotLocked(id string) mcpPolicySnapshot {
	state := c.states[id]
	return mcpPolicySnapshot{
		Failures: state.failures, Cycle: state.cycle, Open: state.open,
		NextAt: state.nextAt, OpenedAt: state.openedAt, PersistFail: c.persistFailed[id],
	}
}

// Allowed 报告自动重连当前是否可以尝试：熔断、持久化失败或尚未到 nextAt 时拒绝。
func (c *MCPRecoveryCoordinator) Allowed(id string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.states[id]
	if !ok {
		return true
	}
	if state.open || c.persistFailed[id] {
		return false
	}
	return state.nextAt.IsZero() || !now.Before(state.nextAt)
}

// RecordSuccess 彻底清除失败状态与熔断；已经是干净状态时不做数据库写入。
func (c *MCPRecoveryCoordinator) RecordSuccess(ctx context.Context, id string) error {
	c.mu.Lock()
	state, ok := c.states[id]
	if !ok {
		state = &mcpPolicyState{}
		c.states[id] = state
	}
	clean := state.failures == 0 && !state.open && state.nextAt.IsZero() && state.openedAt.IsZero()
	state.failures, state.open, state.nextAt, state.openedAt = 0, false, time.Time{}, time.Time{}
	delete(c.persistFailed, id)
	snapshot := c.snapshotLocked(id)
	c.mu.Unlock()
	if clean {
		return nil
	}
	if err := c.persist(ctx, id, snapshot); err != nil {
		return err
	}
	return nil
}

// RecordFailure 记录一次 Prepare 失败：累计到上限后开路，否则进入指数退避。
// 持久化失败时标记该服务停止后续自动尝试并返回错误。
func (c *MCPRecoveryCoordinator) RecordFailure(ctx context.Context, id string, base, maxDelay time.Duration, now time.Time) (mcpPolicySnapshot, error) {
	c.mu.Lock()
	state, ok := c.states[id]
	if !ok {
		state = &mcpPolicyState{}
		c.states[id] = state
	}
	state.failures++
	if state.failures >= mcpCircuitFailureLimit {
		state.open = true
		state.openedAt = now
		state.nextAt = time.Time{}
	} else {
		state.nextAt = now.Add(mcpBackoffDelay(MCPRecoveryOptions{BaseDelay: base, MaxDelay: maxDelay}, state.failures))
	}
	snapshot := c.snapshotLocked(id)
	c.mu.Unlock()
	if err := c.persist(ctx, id, snapshot); err != nil {
		c.mu.Lock()
		c.persistFailed[id] = true
		c.mu.Unlock()
		global.Logger.Error("persist MCP recovery state failed; automatic reconnect stopped for this service",
			zap.String("service_id", id), zap.Error(err))
		return snapshot, err
	}
	return snapshot, nil
}

// StartManualCycle 开启手动重连的新周期：提升 cycle 并清除失败、退避与熔断。
func (c *MCPRecoveryCoordinator) StartManualCycle(ctx context.Context, id string) (mcpPolicySnapshot, error) {
	c.mu.Lock()
	state, ok := c.states[id]
	if !ok {
		state = &mcpPolicyState{}
		c.states[id] = state
	}
	state.cycle++
	state.failures, state.open, state.nextAt, state.openedAt = 0, false, time.Time{}, time.Time{}
	delete(c.persistFailed, id)
	snapshot := c.snapshotLocked(id)
	c.mu.Unlock()
	if err := c.persist(ctx, id, snapshot); err != nil {
		c.mu.Lock()
		c.persistFailed[id] = true
		c.mu.Unlock()
		return snapshot, err
	}
	return snapshot, nil
}

// Clear 在服务停用/删除后清除策略状态，并持久化清零值。
func (c *MCPRecoveryCoordinator) Clear(ctx context.Context, id string) error {
	c.mu.Lock()
	delete(c.states, id)
	delete(c.persistFailed, id)
	c.mu.Unlock()
	return c.persist(ctx, id, mcpPolicySnapshot{})
}

// persist 在协调器锁外写入数据库；行已删除时跳过。
func (c *MCPRecoveryCoordinator) persist(ctx context.Context, id string, snapshot mcpPolicySnapshot) error {
	update := db.EntClient.KaguyaMCPServer.UpdateOneID(id).
		SetReconnectFailures(snapshot.Failures).SetReconnectCircuitOpen(snapshot.Open).SetReconnectCycle(snapshot.Cycle)
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
	if err := update.Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

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
