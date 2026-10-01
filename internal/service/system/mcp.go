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
	ErrMCPNotFound      = errors.New("MCP 服务不存在")
	ErrMCPDuplicate     = errors.New("MCP 服务名称已存在")
	ErrMCPInvalid       = errors.New("MCP 配置无效")
	ErrMCPConnect       = errors.New("MCP 连接或工具发现失败，请检查服务配置")
	ErrMCPDisabled      = errors.New("MCP 服务已停用")
	ErrMCPReconnectBusy = errors.New("MCP 服务正在重连")
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
// 指数退避重试（1s、2s、4s…，上限 60s），直到恢复、停用或删除。
var DefaultMCPRecoveryOptions = MCPRecoveryOptions{Tick: 2 * time.Second, BaseDelay: time.Second, MaxDelay: time.Minute}

// RunMCPRecovery 持续看护已启用但未连接的 MCP 服务，按指数退避自动重连。
// 覆盖启动恢复失败与运行中连接断开两种情况；多次失败后由协调器熔断，
// 服务被停用或删除后停止重试。
func (s *SystemSvc) RunMCPRecovery(ctx context.Context, options MCPRecoveryOptions) {
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
		if agentmcp.Default.Status(id).State == "running" {
			if err := DefaultMCPRecovery.RecordSuccess(ctx, id); err != nil {
				global.Logger.Warn("clear MCP recovery state failed", zap.String("service_id", id))
			}
			continue
		}
		// 熔断、持久化失败或退避未到时跳过；数据库是策略状态的权威来源。
		if !DefaultMCPRecovery.Allowed(id, now) {
			continue
		}
		// 不阻塞整个监督器：用户正在启停或编辑该服务时跳过本轮。
		unlock, ok := mcpMutations.tryAcquire(id)
		if !ok {
			continue
		}
		s.reconnectMCP(ctx, id, options, now)
		unlock()
	}
}

// reconnectMCP 在已持有变更门的情况下尝试一次重连；请求取消与熔断不计入失败。
func (s *SystemSvc) reconnectMCP(ctx context.Context, id string, options MCPRecoveryOptions, now time.Time) {
	row, err := mcpFind(ctx, id)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, ErrMCPNotFound) {
			global.Logger.Warn("load MCP service for recovery failed", zap.String("service_id", id))
		}
		return
	}
	policy := DefaultMCPRecovery.Load(row)
	// 拿到门后再确认服务仍启用且未由其他路径恢复，避免复活刚被停用的服务。
	if !row.Enabled || agentmcp.Default.Status(id).State == "running" || policy.Open {
		return
	}
	connection, err := agentmcp.Prepare(ctx, mcpConfig(row))
	if err == nil {
		agentmcp.Default.Replace(id, connection)
		if policy.Failures > 0 {
			global.Logger.Info("MCP connection recovered",
				zap.String("service_id", id), zap.String("transport", string(row.Transport)), zap.Int("failures", policy.Failures))
		}
		if recordErr := DefaultMCPRecovery.RecordSuccess(ctx, id); recordErr != nil {
			global.Logger.Warn("clear MCP recovery state failed", zap.String("service_id", id))
		}
		return
	}
	if ctx.Err() != nil {
		// 请求/root 取消打断的尝试不计入失败。
		return
	}
	snapshot, recordErr := DefaultMCPRecovery.RecordFailure(ctx, id, options.BaseDelay, options.MaxDelay, now)
	fields := append(mcpFailureFields(id, string(row.Transport), err), zap.Int("failures", snapshot.Failures))
	if snapshot.Open {
		fields = append(fields, zap.Time("circuit_opened_at", snapshot.OpenedAt))
	} else {
		fields = append(fields, zap.Duration("next_retry", snapshot.NextAt.Sub(now)))
	}
	global.Logger.Warn("MCP reconnect attempt failed", fields...)
	if recordErr != nil {
		agentmcp.Default.Failed(id, "重连状态保存失败，已停止自动重连")
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
	return &dtosystem.SystemMCPResp{Config: config, ID: row.ID, Enabled: row.Enabled, Status: agentmcp.Default.Status(row.ID), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
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
		next, err = agentmcp.Prepare(ctx, *req)
		if err != nil {
			logMCPFailure("validate replacement MCP connection failed", id, req.Transport, err)
			return nil, fmt.Errorf("%w: %w", ErrMCPConnect, err)
		}
	}
	row, err := db.EntClient.KaguyaMCPServer.UpdateOneID(id).SetName(req.Name).SetTransport(kaguyamcpserver.Transport(req.Transport)).SetCommand(req.Command).SetArgs(req.Args).SetEnv(req.Env).SetWorkingDirectory(req.WorkingDirectory).SetURL(req.URL).SetHeaders(req.Headers).SetTimeoutSeconds(req.TimeoutSeconds).Save(ctx)
	if err != nil {
		if next != nil {
			next.Close()
		}
		if ent.IsConstraintError(err) {
			return nil, ErrMCPDuplicate
		}
		return nil, err
	}
	agentmcp.Default.Replace(id, next)
	if old.Enabled {
		// 替代连接验证成功才开始新的健康周期，失败不会清除原熔断。
		if err := DefaultMCPRecovery.RecordSuccess(ctx, id); err != nil {
			global.Logger.Warn("clear MCP recovery state failed", zap.String("service_id", id))
		}
	}
	return mcpResponse(row, true), nil
}
func (s *SystemSvc) MCPSetEnabled(ctx context.Context, id string, enabled bool) (*dtosystem.SystemMCPResp, error) {
	release, err := mcpMutations.acquire(ctx, id)
	if err != nil {
		return nil, err
	}
	defer release()
	old, err := mcpFind(ctx, id)
	if err != nil {
		return nil, err
	}
	if enabled && old.Enabled {
		if agentmcp.Default.Status(id).State == "running" {
			return mcpResponse(old, true), nil
		}
		// 旧客户端对“已启用但异常”的服务重新 PUT enabled=true：委托手动重连语义。
		return s.mcpReconnectLocked(ctx, old)
	}
	var next *agentmcp.Connection
	if enabled {
		next, err = agentmcp.Prepare(ctx, mcpConfig(old))
		if err != nil {
			agentmcp.Default.Failed(id, err.Error())
			logMCPFailure("enable MCP connection failed", id, string(old.Transport), err)
			return nil, fmt.Errorf("%w: %w", ErrMCPConnect, err)
		}
	}
	row, err := db.EntClient.KaguyaMCPServer.UpdateOneID(id).SetEnabled(enabled).Save(ctx)
	if err != nil {
		if next != nil {
			next.Close()
		}
		return nil, err
	}
	agentmcp.Default.Replace(id, next)
	if enabled {
		if err := DefaultMCPRecovery.RecordSuccess(ctx, id); err != nil {
			global.Logger.Warn("clear MCP recovery state failed", zap.String("service_id", id))
		}
	} else if err := DefaultMCPRecovery.Clear(ctx, id); err != nil {
		global.Logger.Warn("clear MCP recovery state failed", zap.String("service_id", id))
	}
	return mcpResponse(row, true), nil
}

// MCPReconnect 手动开启一次新连接周期并立即尝试 Prepare。
// 只对已启用服务有效；同一服务的并发手动请求返回忙碌，不做隐式排队。
func (s *SystemSvc) MCPReconnect(ctx context.Context, id string) (*dtosystem.SystemMCPResp, error) {
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
	if _, err := DefaultMCPRecovery.StartManualCycle(ctx, row.ID); err != nil {
		return nil, err
	}
	connection, err := agentmcp.Prepare(ctx, mcpConfig(row))
	if err == nil {
		agentmcp.Default.Replace(row.ID, connection)
		if err := DefaultMCPRecovery.RecordSuccess(ctx, row.ID); err != nil {
			global.Logger.Warn("clear MCP recovery state failed", zap.String("service_id", row.ID))
		}
		return mcpResponse(row, true), nil
	}
	if ctx.Err() == nil {
		if _, recordErr := DefaultMCPRecovery.RecordFailure(ctx, row.ID, DefaultMCPRecoveryOptions.BaseDelay, DefaultMCPRecoveryOptions.MaxDelay, time.Now()); recordErr != nil {
			agentmcp.Default.Failed(row.ID, "重连状态保存失败，已停止自动重连")
		}
	}
	agentmcp.Default.Failed(row.ID, "MCP 重连失败")
	logMCPFailure("manual MCP reconnect failed", row.ID, string(row.Transport), err)
	return nil, fmt.Errorf("%w: %w", ErrMCPConnect, err)
}

func (s *SystemSvc) MCPDelete(ctx context.Context, id string) error {
	release, err := mcpMutations.acquire(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	if _, err := mcpFind(ctx, id); err != nil {
		return err
	}
	// 和项目其他配置保持软删除一致；清除秘密，名称唯一性只约束未删除行。
	_, err = db.EntClient.KaguyaMCPServer.UpdateOneID(id).SetDeletedAt(time.Now()).SetEnabled(false).SetEnv(map[string]string{}).SetHeaders(map[string]string{}).SetArgs([]string{}).SetCommand("").SetURL("").Save(ctx)
	if err != nil {
		return err
	}
	if err := DefaultMCPRecovery.Clear(ctx, id); err != nil {
		global.Logger.Warn("clear MCP recovery state failed", zap.String("service_id", id))
	}
	agentmcp.Default.Replace(id, nil)
	return nil
}

// RestoreMCP 恢复期望启用的服务；单个远端失败不会阻断应用启动。
// 失败的服务由 RunMCPRecovery 按指数退避继续重连。
func (s *SystemSvc) RestoreMCP(ctx context.Context) error {
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
			if !row.Enabled || agentmcp.Default.Status(id).State == "running" {
				return nil
			}
			// 熔断在重启后仍保持打开，启动恢复不自动绕过。
			if DefaultMCPRecovery.Load(row).Open {
				return nil
			}
			connection, err := agentmcp.Prepare(ctx, mcpConfig(row))
			if err != nil {
				agentmcp.Default.Failed(id, err.Error())
				logMCPFailure("restore MCP connection failed", id, string(row.Transport), err)
				if ctx.Err() == nil {
					// 启动恢复失败计入同一连接周期的失败次数。
					DefaultMCPRecovery.RecordFailure(ctx, id, DefaultMCPRecoveryOptions.BaseDelay, DefaultMCPRecoveryOptions.MaxDelay, time.Now())
				}
				return nil
			}
			agentmcp.Default.Replace(id, connection)
			DefaultMCPRecovery.RecordSuccess(ctx, id)
			return nil
		}()
		if err != nil {
			return err
		}
	}
	return nil
}
