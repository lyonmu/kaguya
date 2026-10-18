package system

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"entgo.io/ent/dialect/sql"
	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamcpserver"
	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
)

var (
	ErrMCPNotFound            = errors.New("MCP 服务不存在")
	ErrMCPDuplicate           = errors.New("MCP 服务名称已存在")
	ErrMCPInvalid             = errors.New("MCP 配置无效")
	ErrMCPConnect             = errors.New("MCP 连接或工具发现失败，请检查服务配置")
	ErrMCPDisabled            = errors.New("MCP 服务已停用")
	ErrMCPReconnectBusy       = errors.New("MCP 服务正在重连")
	ErrMCPRecoveryPersistence = errors.New("MCP 重连状态保存失败，已停止自动重连")
	ErrMCPLifecycle           = errors.New("MCP 运行时生命周期未初始化")
)

// mcpMutationGate 按服务 ID 串行化“准备 → 保存 → 发布”，避免单个故障服务的
// 连接准备阻塞其他 MCP 的启停；等待可被请求 context 取消。
type mcpMutationGate struct {
	mu    sync.Mutex
	locks map[string]*mcpServiceLock
}

type mcpServiceLock struct {
	gate chan struct{}
	refs int
}

func (g *mcpMutationGate) acquire(ctx context.Context, id string) (func(), error) {
	g.mu.Lock()
	lock := g.locks[id]
	if lock == nil {
		lock = &mcpServiceLock{gate: make(chan struct{}, 1)}
		g.locks[id] = lock
	}
	lock.refs++
	g.mu.Unlock()
	for {
		select {
		case lock.gate <- struct{}{}:
			if err := ctx.Err(); err != nil {
				<-lock.gate
				g.releaseLock(id, lock)
				return nil, err
			}
			return func() { <-lock.gate; g.releaseLock(id, lock) }, nil
		case <-ctx.Done():
			g.releaseLock(id, lock)
			return nil, ctx.Err()
		}
	}
}

// tryAcquire 非阻塞地获取服务门；已被占用时立即返回 false，
// 供后台监督器跳过用户正在启停或编辑的服务。
func (g *mcpMutationGate) tryAcquire(id string) (func(), bool) {
	g.mu.Lock()
	lock := g.locks[id]
	if lock == nil {
		lock = &mcpServiceLock{gate: make(chan struct{}, 1)}
		g.locks[id] = lock
	}
	lock.refs++
	g.mu.Unlock()
	select {
	case lock.gate <- struct{}{}:
		return func() { <-lock.gate; g.releaseLock(id, lock) }, true
	default:
		g.releaseLock(id, lock)
		return nil, false
	}
}

// releaseLock 释放门并回收已无引用者的锁对象。
func (g *mcpMutationGate) releaseLock(id string, lock *mcpServiceLock) {
	g.mu.Lock()
	lock.refs--
	if lock.refs == 0 {
		delete(g.locks, id)
	}
	g.mu.Unlock()
}

var mcpMutations = mcpMutationGate{locks: map[string]*mcpServiceLock{}}

// mcpFailureFields 组装可安全记录的失败字段，不包含 URL、命令、请求头和响应正文。
func mcpFailureFields(id, transport string, err error) []zap.Field {
	stage, reason := agentmcp.Diagnostic(err)
	fields := []zap.Field{zap.String("service_id", id), zap.String("transport", transport), zap.String("stage", stage), zap.String("reason", reason)}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		fields = append(fields, zap.Int("exit_code", exitErr.ExitCode()))
	}
	return fields
}

func logMCPFailure(message, id, transport string, err error) {
	global.Logger.Warn(message, mcpFailureFields(id, transport, err)...)
}

// MCPRecoveryOptions 控制 MCP 连接自动重连监督器的调度。
type MCPRecoveryOptions struct {
	Tick      time.Duration
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

// DefaultMCPRecoveryOptions 是默认重连调度：每 2 秒检查一次，单个服务按
// 指数退避重试（1s、2s、4s…，上限 60s），连续 5 次失败熔断。
var DefaultMCPRecoveryOptions = MCPRecoveryOptions{Tick: 2 * time.Second, BaseDelay: time.Second, MaxDelay: time.Minute}

// RunMCPRecovery 持续看护已启用但未连接的 MCP 服务，按指数退避自动重连。
// 覆盖启动恢复失败与运行中连接断开两种情况；多次失败后由协调器熔断，
// 服务被停用或删除后停止重试。
func (s *SystemSvc) RunMCPRecovery(ctx context.Context, options MCPRecoveryOptions) {
	ctx, done, err := s.mcpRequestContext(ctx)
	if err != nil {
		return
	}
	defer done()
	options = normalizeMCPRecoveryOptions(options)
	ticker := time.NewTicker(options.Tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.recoverMCPConnections(ctx, now, options)
		}
	}
}

func normalizeMCPRecoveryOptions(options MCPRecoveryOptions) MCPRecoveryOptions {
	if options.Tick <= 0 {
		options.Tick = DefaultMCPRecoveryOptions.Tick
	}
	if options.BaseDelay <= 0 {
		options.BaseDelay = DefaultMCPRecoveryOptions.BaseDelay
	}
	if options.MaxDelay < options.BaseDelay {
		options.MaxDelay = options.BaseDelay
	}
	return options
}

// recoverMCPConnections 执行一轮重连检查，单轮错误不影响后续轮次。
func (s *SystemSvc) recoverMCPConnections(ctx context.Context, now time.Time, options MCPRecoveryOptions) {
	ids, err := db.EntClient.KaguyaMCPServer.Query().Where(kaguyamcpserver.DeletedAtIsNil(), kaguyamcpserver.EnabledEQ(true)).IDs(ctx)
	if err != nil {
		if ctx.Err() == nil {
			global.Logger.Warn("query MCP services for recovery failed")
		}
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		// 健康检查也持有同服务门，不能覆盖手动重连的新周期。
		unlock, ok := mcpMutations.tryAcquire(id)
		if !ok {
			continue
		}
		s.reconnectMCP(ctx, id, options, now)
		unlock()
	}
}

// reconnectMCP 的调用者持有同服务门；拿到门后再确认策略、启用和取消状态。
func (s *SystemSvc) reconnectMCP(ctx context.Context, id string, options MCPRecoveryOptions, now time.Time) {
	row, err := mcpFind(ctx, id)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, ErrMCPNotFound) {
			global.Logger.Warn("load MCP service for recovery failed", zap.String("service_id", id))
		}
		return
	}
	s.connectAutomatic(ctx, row, options, now, "MCP reconnect attempt failed")
}

func (s *SystemSvc) prepareMCP(ctx context.Context, id string, config agentmcp.Config) (*agentmcp.Connection, error) {
	DefaultMCPRecovery.setConnecting(id, true)
	defer DefaultMCPRecovery.setConnecting(id, false)
	return agentmcp.PrepareWithLifetime(ctx, s.mcpRoot, config)
}

// 连接发布前重新读真实持久行，不能把迟到候选发布到取消、停用、删除或新周期。
func (s *SystemSvc) publishMCP(ctx context.Context, id string, cycle int64, next *agentmcp.Connection) (*ent.KaguyaMCPServer, error) {
	row, err := s.checkMCPPublication(ctx, id, cycle, true)
	if err == nil {
		err = next.Err()
	}
	if err != nil {
		next.Close()
		return nil, err
	}
	agentmcp.Default.Replace(id, next)
	return row, nil
}

func (s *SystemSvc) checkMCPPublication(ctx context.Context, id string, cycle int64, enabled bool) (*ent.KaguyaMCPServer, error) {
	row, err := mcpFind(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.mcpRoot.Err(); err != nil {
		return nil, err
	}
	if row.Enabled != enabled {
		return nil, ErrMCPDisabled
	}
	if row.ReconnectCycle != cycle {
		return nil, ErrMCPReconnectBusy
	}
	return row, nil
}

func (s *SystemSvc) connectAutomatic(ctx context.Context, row *ent.KaguyaMCPServer, options MCPRecoveryOptions, now time.Time, failureMessage string) {
	policy := DefaultMCPRecovery.Load(row)
	if !row.Enabled || ctx.Err() != nil || s.mcpRoot.Err() != nil {
		return
	}
	if agentmcp.Default.Status(row.ID).State == "running" {
		if !policy.PersistFail {
			_ = DefaultMCPRecovery.RecordSuccess(ctx, row.ID)
		}
		return
	}
	if !DefaultMCPRecovery.Allowed(row.ID, now) {
		return
	}
	connection, err := s.prepareMCP(ctx, row.ID, mcpConfig(row))
	if err == nil {
		if _, err := s.checkMCPPublication(ctx, row.ID, policy.Cycle, true); err != nil {
			connection.Close()
			return
		}
		if err := DefaultMCPRecovery.RecordSuccess(ctx, row.ID); err != nil {
			connection.Close()
			return
		}
		if _, err := s.publishMCP(ctx, row.ID, policy.Cycle, connection); err != nil {
			return
		}
		if policy.Failures > 0 {
			global.Logger.Info("MCP connection recovered", zap.String("service_id", row.ID), zap.String("transport", string(row.Transport)), zap.Int("failures", policy.Failures))
		}
		return
	}
	if ctx.Err() != nil || s.mcpRoot.Err() != nil {
		return
	}
	snapshot, recordErr := DefaultMCPRecovery.RecordFailure(ctx, row.ID, options.BaseDelay, options.MaxDelay, now)
	fields := append(mcpFailureFields(row.ID, string(row.Transport), err), zap.Int("failures", snapshot.Failures))
	if snapshot.Open {
		fields = append(fields, zap.Time("circuit_opened_at", snapshot.OpenedAt))
	} else if !snapshot.NextAt.IsZero() {
		fields = append(fields, zap.Duration("next_retry", snapshot.NextAt.Sub(now)))
	}
	global.Logger.Warn(failureMessage, fields...)
	if recordErr == nil {
		agentmcp.Default.Failed(row.ID, "MCP 连接失败")
	}
}

func mcpConfig(row *ent.KaguyaMCPServer) agentmcp.Config {
	return agentmcp.Config{Name: row.Name, Transport: string(row.Transport), Command: row.Command, Args: row.Args, Env: row.Env, WorkingDirectory: row.WorkingDirectory, URL: row.URL, Headers: row.Headers, TimeoutSeconds: row.TimeoutSeconds}
}
func mcpResponse(row *ent.KaguyaMCPServer, detail bool) *dtosystem.SystemMCPResp {
	config := mcpConfig(row)
	// 列表不传送环境变量和认证请求头；仅在编辑详情中读取。
	if !detail {
		config.Env = nil
		config.Headers = nil
	}
	policy := DefaultMCPRecovery.Load(row)
	status := agentmcp.Default.Status(row.ID)
	phase := "stopped"
	switch {
	case policy.PersistFail:
		phase = "storage_error"
	case !row.Enabled:
		phase = "stopped"
	case policy.Connecting:
		phase = "connecting"
	case status.State == "running":
		phase = "running"
	case policy.Open:
		phase = "circuit_open"
	case !policy.NextAt.IsZero():
		phase = "backoff"
	case row.Enabled:
		phase = "connecting"
	}
	resp := &dtosystem.SystemMCPResp{Config: config, ID: row.ID, Enabled: row.Enabled, Status: status, Phase: phase, ConsecutiveFailures: policy.Failures, ReconnectCycle: policy.Cycle, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if !policy.NextAt.IsZero() {
		at := policy.NextAt
		resp.NextRetryAt = &at
	}
	if !policy.OpenedAt.IsZero() {
		at := policy.OpenedAt
		resp.CircuitOpenedAt = &at
	}
	return resp
}
func mcpFind(ctx context.Context, id string) (*ent.KaguyaMCPServer, error) {
	row, err := db.EntClient.KaguyaMCPServer.Query().Where(kaguyamcpserver.IDEQ(id), kaguyamcpserver.DeletedAtIsNil()).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrMCPNotFound
	}
	return row, err
}
func (s *SystemSvc) MCPPage(ctx context.Context, req *dtosystem.SystemMCPPageReq) (*dtosystem.SystemMCPListResp, error) {
	if req.Page < 1 || req.PageSize < 1 || req.PageSize > 100 {
		return nil, ErrMCPInvalid
	}
	query := db.EntClient.KaguyaMCPServer.Query().Where(kaguyamcpserver.DeletedAtIsNil())
	if req.Name != "" {
		query.Where(kaguyamcpserver.NameContains(req.Name))
	}
	total, err := query.Count(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := query.Order(kaguyamcpserver.ByCreatedAt(sql.OrderDesc()), kaguyamcpserver.ByID()).Offset((req.Page - 1) * req.PageSize).Limit(req.PageSize).All(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]*dtosystem.SystemMCPResp, 0, len(rows))
	for _, row := range rows {
		items = append(items, mcpResponse(row, false))
	}
	return &dtosystem.SystemMCPListResp{Items: items, Total: total, Page: req.Page, PageSize: req.PageSize}, nil
}
func (s *SystemSvc) MCPDetail(ctx context.Context, id string) (*dtosystem.SystemMCPResp, error) {
	row, err := mcpFind(ctx, id)
	if err != nil {
		return nil, err
	}
	return mcpResponse(row, true), nil
}

func (s *SystemSvc) MCPCreate(ctx context.Context, req *dtosystem.SystemMCPSaveReq) (*dtosystem.SystemMCPResp, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrMCPInvalid, err)
	}
	// 新建服务尚未发布连接，名称唯一性由数据库约束保障，无需全局锁。
	row, err := db.EntClient.KaguyaMCPServer.Create().SetName(req.Name).SetTransport(kaguyamcpserver.Transport(req.Transport)).SetCommand(req.Command).SetArgs(req.Args).SetEnv(req.Env).SetWorkingDirectory(req.WorkingDirectory).SetURL(req.URL).SetHeaders(req.Headers).SetTimeoutSeconds(req.TimeoutSeconds).Save(ctx)
	if ent.IsConstraintError(err) {
		return nil, ErrMCPDuplicate
	}
	if err != nil {
		return nil, err
	}
	return mcpResponse(row, true), nil
}
func (s *SystemSvc) MCPUpdate(ctx context.Context, id string, req *dtosystem.SystemMCPSaveReq) (*dtosystem.SystemMCPResp, error) {
	ctx, done, err := s.mcpRequestContext(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrMCPInvalid, err)
	}
	release, err := mcpMutations.acquire(ctx, id)
	if err != nil {
		return nil, err
	}
	defer release()
	old, err := mcpFind(ctx, id)
	if err != nil {
		return nil, err
	}
	var next *agentmcp.Connection
	if old.Enabled {
		next, err = s.prepareMCP(ctx, id, *req)
		if err != nil {
			logMCPFailure("validate replacement MCP connection failed", id, req.Transport, err)
			return nil, fmt.Errorf("%w: %w", ErrMCPConnect, err)
		}
	}
	if _, err := s.checkMCPPublication(ctx, id, old.ReconnectCycle, old.Enabled); err != nil {
		if next != nil {
			next.Close()
		}
		return nil, err
	}
	DefaultMCPRecovery.Load(old)
	candidate := DefaultMCPRecovery.newCycle(old)
	update := db.EntClient.KaguyaMCPServer.UpdateOneID(id).Where(kaguyamcpserver.DeletedAtIsNil(), kaguyamcpserver.ReconnectCycleEQ(old.ReconnectCycle), kaguyamcpserver.EnabledEQ(old.Enabled)).SetName(req.Name).SetTransport(kaguyamcpserver.Transport(req.Transport)).SetCommand(req.Command).SetArgs(req.Args).SetEnv(req.Env).SetWorkingDirectory(req.WorkingDirectory).SetURL(req.URL).SetHeaders(req.Headers).SetTimeoutSeconds(req.TimeoutSeconds)
	row, persistErr := policyUpdate(update, candidate).Save(ctx)
	if ent.IsConstraintError(persistErr) {
		if next != nil {
			next.Close()
		}
		return nil, ErrMCPDuplicate
	}
	_, err = DefaultMCPRecovery.confirm(id, candidate, persistErr, true)
	if err != nil {
		if next != nil {
			next.Close()
		}
		return nil, err
	}
	if next != nil {
		row, err = s.publishMCP(ctx, id, candidate.Cycle, next)
		if err != nil {
			// 配置提交已确认，迟到取消不能留下使用旧配置的健康连接。
			// 候选已关闭；监督器后续只按新持久配置恢复。
			agentmcp.Default.Replace(id, nil)
			return nil, err
		}
	} else {
		agentmcp.Default.Replace(id, nil)
	}
	return mcpResponse(row, true), nil
}

func (s *SystemSvc) MCPSetEnabled(ctx context.Context, id string, enabled bool) (*dtosystem.SystemMCPResp, error) {
	ctx, done, err := s.mcpRequestContext(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	release, err := mcpMutations.acquire(ctx, id)
	if err != nil {
		return nil, err
	}
	defer release()
	old, err := mcpFind(ctx, id)
	if err != nil {
		return nil, err
	}
	DefaultMCPRecovery.Load(old)
	if enabled && old.Enabled {
		policy, _ := DefaultMCPRecovery.Snapshot(id)
		if agentmcp.Default.Status(id).State == "running" && !policy.PersistFail {
			return mcpResponse(old, true), nil
		}
		return s.mcpReconnectLocked(ctx, old)
	}
	if !enabled {
		row, err := db.EntClient.KaguyaMCPServer.UpdateOneID(id).SetEnabled(false).Save(ctx)
		if err != nil {
			return nil, DefaultMCPRecovery.storageFailure(id, err)
		}
		// 先确认停用，再关闭；策略清理失败也不能留下可用旧连接。
		agentmcp.Default.Replace(id, nil)
		if err := DefaultMCPRecovery.Clear(ctx, id); err != nil {
			return nil, err
		}
		return mcpResponse(row, true), nil
	}
	policy, err := DefaultMCPRecovery.StartManualCycle(ctx, id)
	if err != nil {
		return nil, err
	}
	next, err := s.prepareMCP(ctx, id, mcpConfig(old))
	if err != nil {
		if ctx.Err() == nil {
			_, _ = DefaultMCPRecovery.RecordFailure(ctx, id, DefaultMCPRecoveryOptions.BaseDelay, DefaultMCPRecoveryOptions.MaxDelay, time.Now())
			agentmcp.Default.Failed(id, "MCP 连接失败")
		}
		logMCPFailure("enable MCP connection failed", id, string(old.Transport), err)
		return nil, fmt.Errorf("%w: %w", ErrMCPConnect, err)
	}
	if _, err := s.checkMCPPublication(ctx, id, policy.Cycle, false); err != nil {
		next.Close()
		return nil, err
	}
	if err := DefaultMCPRecovery.RecordSuccess(ctx, id); err != nil {
		next.Close()
		return nil, err
	}
	row, err := db.EntClient.KaguyaMCPServer.UpdateOneID(id).Where(kaguyamcpserver.DeletedAtIsNil(), kaguyamcpserver.EnabledEQ(false), kaguyamcpserver.ReconnectCycleEQ(policy.Cycle)).SetEnabled(true).Save(ctx)
	if err != nil {
		next.Close()
		return nil, DefaultMCPRecovery.storageFailure(id, err)
	}
	row, err = s.publishMCP(ctx, id, policy.Cycle, next)
	if err != nil {
		return nil, err
	}
	return mcpResponse(row, true), nil
}

// MCPReconnect 手动开启一次新连接周期并立即尝试 Prepare。
// 只对已启用服务有效；同一服务的并发手动请求返回忙碌，不做隐式排队。
func (s *SystemSvc) MCPReconnect(ctx context.Context, id string) (*dtosystem.SystemMCPResp, error) {
	ctx, done, err := s.mcpRequestContext(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	unlock, ok := mcpMutations.tryAcquire(id)
	if !ok {
		return nil, ErrMCPReconnectBusy
	}
	defer unlock()
	row, err := mcpFind(ctx, id)
	if err != nil {
		return nil, err
	}
	if !row.Enabled {
		return nil, ErrMCPDisabled
	}
	return s.mcpReconnectLocked(ctx, row)
}

// mcpReconnectLocked 在已持有服务门时执行手动新周期：先持久化 cycle/清零熔断，
// 再尝试一次 Prepare；失败进入新周期的第 1 次退避，仍由监督器继续。
func (s *SystemSvc) mcpReconnectLocked(ctx context.Context, row *ent.KaguyaMCPServer) (*dtosystem.SystemMCPResp, error) {
	DefaultMCPRecovery.Load(row)
	policy, err := DefaultMCPRecovery.StartManualCycle(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	// 明确重连在新周期已确认后停掉旧连接，防止旧健康连接在下一次
	// tick 把新周期的失败误清零；周期保存失败时原连接保持可用。
	agentmcp.Default.Replace(row.ID, nil)
	connection, err := s.prepareMCP(ctx, row.ID, mcpConfig(row))
	if err == nil {
		if _, err := s.checkMCPPublication(ctx, row.ID, policy.Cycle, true); err != nil {
			connection.Close()
			return nil, err
		}
		if err := DefaultMCPRecovery.RecordSuccess(ctx, row.ID); err != nil {
			connection.Close()
			return nil, err
		}
		current, err := s.publishMCP(ctx, row.ID, policy.Cycle, connection)
		if err != nil {
			return nil, err
		}
		return mcpResponse(current, true), nil
	}
	if ctx.Err() == nil {
		_, _ = DefaultMCPRecovery.RecordFailure(ctx, row.ID, DefaultMCPRecoveryOptions.BaseDelay, DefaultMCPRecoveryOptions.MaxDelay, time.Now())
		agentmcp.Default.Failed(row.ID, "MCP 重连失败")
	}
	logMCPFailure("manual MCP reconnect failed", row.ID, string(row.Transport), err)
	return nil, fmt.Errorf("%w: %w", ErrMCPConnect, err)
}

func (s *SystemSvc) MCPDelete(ctx context.Context, id string) error {
	ctx, done, err := s.mcpRequestContext(ctx)
	if err != nil {
		return err
	}
	defer done()
	release, err := mcpMutations.acquire(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	row, err := mcpFind(ctx, id)
	if err != nil {
		return err
	}
	DefaultMCPRecovery.Load(row)
	// 和项目其他配置保持软删除一致；清除秘密，名称唯一性只约束未删除行。
	_, err = db.EntClient.KaguyaMCPServer.UpdateOneID(id).SetDeletedAt(time.Now()).SetEnabled(false).SetEnv(map[string]string{}).SetHeaders(map[string]string{}).SetArgs([]string{}).SetCommand("").SetURL("").Save(ctx)
	if err != nil {
		return DefaultMCPRecovery.storageFailure(id, err)
	}
	agentmcp.Default.Replace(id, nil)
	if err := DefaultMCPRecovery.Clear(ctx, id); err != nil {
		return err
	}
	DefaultMCPRecovery.forgetDeleted(id)
	return nil
}

// RestoreMCP 恢复期望启用的服务；单个远端失败不会阻断应用启动。
// 失败的服务由 RunMCPRecovery 按指数退避继续重连。
func (s *SystemSvc) RestoreMCP(ctx context.Context) error {
	ctx, done, err := s.mcpRequestContext(ctx)
	if err != nil {
		return err
	}
	defer done()
	ids, err := db.EntClient.KaguyaMCPServer.Query().Where(kaguyamcpserver.DeletedAtIsNil(), kaguyamcpserver.EnabledEQ(true)).IDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 每个服务独立持锁，允许用户在启动恢复过程中停用或删除其他配置。
		err := func() error {
			release, err := mcpMutations.acquire(ctx, id)
			if err != nil {
				return err
			}
			defer release()
			row, err := mcpFind(ctx, id)
			if errors.Is(err, ErrMCPNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			s.connectAutomatic(ctx, row, DefaultMCPRecoveryOptions, time.Now(), "restore MCP connection failed")
			return nil
		}()
		if err != nil {
			return err
		}
	}
	return nil
}
