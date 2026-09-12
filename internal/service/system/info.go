package system

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamodelsinfo"
	"github.com/lyonmu/kaguya/internal/ent/kaguyasysteminfo"
)

var ErrInvalidSystemInfo = errors.New("invalid system configuration")

// Info 每次从数据库读取，不使用进程缓存；新请求立即使用已保存配置。
// 基础数据由 internal/init 在启动阶段创建，查询接口不初始化或迁移数据。
func (s *SystemSvc) Info(ctx context.Context) (*dtosystem.SystemInfoResp, error) {
	client := db.EntClient
	row, err := client.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil {
		return nil, err
	}
	return systemInfoResponse(row), nil
}

func (s *SystemSvc) InfoUpdate(ctx context.Context, req *dtosystem.SystemInfoSaveReq) (*dtosystem.SystemInfoResp, error) {
	if req.ContextCompactionPercent != nil && (*req.ContextCompactionPercent < 10 || *req.ContextCompactionPercent > 95) {
		return nil, ErrInvalidSystemInfo
	}
	if utf8.RuneCountInString(req.SystemPrompt) > 20000 || len(req.DefaultModelID) > 64 || len(req.TaskModelID) > 64 || strings.TrimSpace(req.UserAgent) == "" || len(req.UserAgent) > 512 {
		return nil, ErrInvalidSystemInfo
	}
	// HTTP 请求头只能使用可打印 ASCII，拒绝 CR/LF 等控制字符，防止头注入。
	for _, c := range []byte(req.UserAgent) {
		if c < 32 || c > 126 {
			return nil, ErrInvalidSystemInfo
		}
	}
	if req.AgentMaxSteps != nil && (*req.AgentMaxSteps < 0 || *req.AgentMaxSteps > 1000) || req.CommandTimeoutSeconds != nil && (*req.CommandTimeoutSeconds < 1 || *req.CommandTimeoutSeconds > 86400) || req.ChatMaxRetries != nil && (*req.ChatMaxRetries < 0 || *req.ChatMaxRetries > 20) || len(req.GlobalAgentsPaths) > 32 {
		return nil, ErrInvalidSystemInfo
	}
	for _, path := range req.GlobalAgentsPaths {
		if len(path) > 4096 || strings.ContainsAny(path, "\x00\r\n") || !(filepath.IsAbs(path) || strings.HasPrefix(path, "~/")) {
			return nil, ErrInvalidSystemInfo
		}
	}
	if _, err := s.Info(ctx); err != nil {
		return nil, err
	}
	tx, err := db.EntClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// 先取得单例行的写锁，再检查模型；与删除时清空选择的事务串行化。
	// 校验失败时整体回滚，未提交配置不会对其他请求可见。
	update := tx.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).
		SetSystemPrompt(req.SystemPrompt).SetUserAgent(req.UserAgent).
		SetDefaultModelID(req.DefaultModelID).SetTaskModelID(req.TaskModelID)
	if req.AgentMaxSteps != nil {
		update.SetAgentMaxSteps(*req.AgentMaxSteps)
	}
	if req.ContextCompactionPercent != nil {
		update.SetContextCompactionPercent(*req.ContextCompactionPercent)
	}
	if req.CommandTimeoutSeconds != nil {
		update.SetCommandTimeoutSeconds(*req.CommandTimeoutSeconds)
	}
	if req.ChatMaxRetries != nil {
		update.SetChatMaxRetries(*req.ChatMaxRetries)
	}
	if req.GlobalAgentsPaths != nil {
		update.SetGlobalAgentsPaths(req.GlobalAgentsPaths)
	}
	row, err := update.Save(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range []string{req.DefaultModelID, req.TaskModelID} {
		if id == "" {
			continue
		}
		exists, err := modelQuery(tx.Client()).Where(kaguyamodelsinfo.IDEQ(id)).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrModelNotFound
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return systemInfoResponse(row), nil
}

func systemInfoResponse(row *ent.KaguyaSystemInfo) *dtosystem.SystemInfoResp {
	return &dtosystem.SystemInfoResp{
		SystemInfoSaveReq: dtosystem.SystemInfoSaveReq{
			ContextCompactionPercent: &row.ContextCompactionPercent,
			SystemPrompt:             row.SystemPrompt, UserAgent: row.UserAgent,
			AgentMaxSteps: &row.AgentMaxSteps, CommandTimeoutSeconds: &row.CommandTimeoutSeconds, ChatMaxRetries: &row.ChatMaxRetries, GlobalAgentsPaths: defaultAgentsPaths(row.GlobalAgentsPaths),
			DefaultModelID: row.DefaultModelID, TaskModelID: row.TaskModelID,
		},
		GlobalSystemPrompt: consts.GlobalSystemPrompt,
	}
}

// ChatSystemPrompt 始终保留基础人设，避免空自定义配置导致没有系统提示词。
func ChatSystemPrompt(custom string) string {
	if strings.TrimSpace(custom) == "" {
		return consts.GlobalSystemPrompt
	}
	return consts.GlobalSystemPrompt + "\n\n" + custom
}

// 删除模型/提供商时，在同一事务内取消相关全局选择。
func clearModelSelections(ctx context.Context, client *ent.Client, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := client.KaguyaSystemInfo.Update().Where(kaguyasysteminfo.DefaultModelIDIn(ids...)).SetDefaultModelID("").Exec(ctx); err != nil {
		return err
	}
	return client.KaguyaSystemInfo.Update().Where(kaguyasysteminfo.TaskModelIDIn(ids...)).SetTaskModelID("").Exec(ctx)
}

func defaultAgentsPaths(paths []string) []string {
	if paths == nil {
		return []string{"~/.config/agents/AGENTS.md", "~/.codex/AGENTS.md"}
	}
	return paths
}
