package system

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	ErrMCPNotFound  = errors.New("MCP 服务不存在")
	ErrMCPDuplicate = errors.New("MCP 服务名称已存在")
	ErrMCPInvalid   = errors.New("MCP 配置无效")
	ErrMCPConnect   = errors.New("MCP 连接或工具发现失败，请检查服务配置")
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

// mcpRecoveryState 记录单个服务的连续失败次数与下一次尝试时间。
type mcpRecoveryState struct {
	failures    int
	nextAttempt time.Time
}

// RunMCPRecovery 持续看护已启用但未连接的 MCP 服务，按指数退避自动重连。
// 覆盖启动恢复失败与运行中连接断开两种情况；服务被停用或删除后停止重试。
func (s *SystemSvc) RunMCPRecovery(ctx context.Context, options MCPRecoveryOptions) {
	if options.Tick <= 0 {
		options.Tick = DefaultMCPRecoveryOptions.Tick
	}
	if options.BaseDelay <= 0 {
		options.BaseDelay = DefaultMCPRecoveryOptions.BaseDelay
	}
	if options.MaxDelay < options.BaseDelay {
		options.MaxDelay = options.BaseDelay
	}
	ticker := time.NewTicker(options.Tick)
	defer ticker.Stop()
	states := map[string]*mcpRecoveryState{}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.recoverMCPConnections(ctx, now, options, states)
		}
	}
}

// recoverMCPConnections 执行一轮重连检查，单轮错误不影响后续轮次。
func (s *SystemSvc) recoverMCPConnections(ctx context.Context, now time.Time, options MCPRecoveryOptions, states map[string]*mcpRecoveryState) {
	ids, err := db.EntClient.KaguyaMCPServer.Query().Where(kaguyamcpserver.DeletedAtIsNil(), kaguyamcpserver.EnabledEQ(true)).IDs(ctx)
	if err != nil {
		if ctx.Err() == nil {
			global.Logger.Warn("query MCP services for recovery failed")
		}
		return
	}
	active := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		active[id] = struct{}{}
		// 连接健康时清除退避状态；再次断开后从最短延迟重新开始。
		if agentmcp.Default.Status(id).State == "running" {
			delete(states, id)
			continue
		}
		state := states[id]
		if state == nil {
			state = &mcpRecoveryState{}
			states[id] = state
		}
		if now.Before(state.nextAttempt) {
			continue
		}
		// 不阻塞整个监督器：用户正在启停或编辑该服务时跳过本轮。
		unlock, ok := mcpMutations.tryAcquire(id)
		if !ok {
			continue
		}
		s.reconnectMCP(ctx, id, state, options, now)
		unlock()
	}
	for id := range states {
		if _, ok := active[id]; !ok {
			delete(states, id)
		}
	}
}

// reconnectMCP 在已持有变更门的情况下尝试一次重连。
func (s *SystemSvc) reconnectMCP(ctx context.Context, id string, state *mcpRecoveryState, options MCPRecoveryOptions, now time.Time) {
	row, err := mcpFind(ctx, id)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, ErrMCPNotFound) {
			global.Logger.Warn("load MCP service for recovery failed", zap.String("service_id", id))
		}
		return
	}
	// 拿到门后再确认服务仍启用且未由其他路径恢复，避免复活刚被停用的服务。
	if !row.Enabled || agentmcp.Default.Status(id).State == "running" {
		return
	}
	connection, err := agentmcp.Prepare(ctx, mcpConfig(row))
	if err == nil {
		agentmcp.Default.Replace(id, connection)
		if state.failures > 0 {
			global.Logger.Info("MCP connection recovered",
				zap.String("service_id", id), zap.String("transport", string(row.Transport)), zap.Int("failures", state.failures))
		}
		state.failures = 0
		return
	}
	state.failures++
	delay := mcpBackoffDelay(options, state.failures)
	state.nextAttempt = now.Add(delay)
	global.Logger.Warn("MCP reconnect attempt failed", append(mcpFailureFields(id, string(row.Transport), err),
		zap.Int("failures", state.failures), zap.Duration("next_retry", delay))...)
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
	if enabled && old.Enabled && agentmcp.Default.Status(id).State == "running" {
		return mcpResponse(old, true), nil
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
	return mcpResponse(row, true), nil
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
			connection, err := agentmcp.Prepare(ctx, mcpConfig(row))
			if err != nil {
				agentmcp.Default.Failed(id, err.Error())
				logMCPFailure("restore MCP connection failed", id, string(row.Transport), err)
				return nil
			}
			agentmcp.Default.Replace(id, connection)
			return nil
		}()
		if err != nil {
			return err
		}
	}
	return nil
}
