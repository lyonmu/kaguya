package system

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"entgo.io/ent/dialect/sql"
	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamcpserver"
	"github.com/lyonmu/kaguya/internal/global"
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
	drop := func() {
		g.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(g.locks, id)
		}
		g.mu.Unlock()
	}
	for {
		select {
		case lock.gate <- struct{}{}:
			return func() { <-lock.gate; drop() }, nil
		case <-ctx.Done():
			drop()
			return nil, ctx.Err()
		}
	}
}

var mcpMutations = mcpMutationGate{locks: map[string]*mcpServiceLock{}}

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
			return nil, fmt.Errorf("%w: %s", ErrMCPConnect, err)
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
			return nil, fmt.Errorf("%w: %s", ErrMCPConnect, err)
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
	// 和项目其他配置保持软删除一致；清除秘密，释放名称以便重建。
	_, err = db.EntClient.KaguyaMCPServer.UpdateOneID(id).SetDeletedAt(time.Now()).SetEnabled(false).SetName("deleted-" + id).SetEnv(map[string]string{}).SetHeaders(map[string]string{}).SetArgs([]string{}).SetCommand("").SetURL("").Save(ctx)
	if err != nil {
		return err
	}
	agentmcp.Default.Replace(id, nil)
	return nil
}

// RestoreMCP 恢复期望启用的服务；单个远端失败不会阻断应用启动。
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
				global.Logger.Sugar().Warnf("restore MCP connection failed: id=%s", id)
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
