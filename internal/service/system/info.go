package system

import (
	"context"
	"errors"
	"net/url"
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
	row, err := client.KaguyaSystemInfo.Query().Where(kaguyasysteminfo.IDEQ(consts.SystemInfoID)).
		Select(
			kaguyasysteminfo.FieldID, kaguyasysteminfo.FieldAgentMaxSteps,
			kaguyasysteminfo.FieldContextCompactionPercent, kaguyasysteminfo.FieldCommandTimeoutSeconds,
			kaguyasysteminfo.FieldChatMaxRetries, kaguyasysteminfo.FieldGlobalAgentsPaths,
			kaguyasysteminfo.FieldGlobalSystemPrompt, kaguyasysteminfo.FieldSystemPrompt,
			kaguyasysteminfo.FieldModelSyncEnabled, kaguyasysteminfo.FieldModelSyncURL, kaguyasysteminfo.FieldProviderSyncURL, kaguyasysteminfo.FieldModelSyncIntervalHours,
			kaguyasysteminfo.FieldModelCatalogCount, kaguyasysteminfo.FieldProviderCatalogCount, kaguyasysteminfo.FieldModelSyncLastAttemptAt,
			kaguyasysteminfo.FieldModelSyncLastSuccessAt, kaguyasysteminfo.FieldModelSyncLastError,
			kaguyasysteminfo.FieldDefaultModelID, kaguyasysteminfo.FieldTaskModelID,
		).Only(ctx)
	if err != nil {
		return nil, err
	}
	return systemInfoResponse(row), nil
}

func (s *SystemSvc) InfoUpdate(ctx context.Context, req *dtosystem.SystemInfoSaveReq) (*dtosystem.SystemInfoResp, error) {
	if req.ContextCompactionPercent != nil && (*req.ContextCompactionPercent < 10 || *req.ContextCompactionPercent > 95) {
		return nil, ErrInvalidSystemInfo
	}
	if utf8.RuneCountInString(req.SystemPrompt) > 20000 || req.GlobalSystemPrompt != nil && utf8.RuneCountInString(*req.GlobalSystemPrompt) > 20000 || len(req.DefaultModelID) > 64 || len(req.TaskModelID) > 64 {
		return nil, ErrInvalidSystemInfo
	}
	if req.AgentMaxSteps != nil && (*req.AgentMaxSteps < 0 || *req.AgentMaxSteps > 1000) || req.CommandTimeoutSeconds != nil && (*req.CommandTimeoutSeconds < 1 || *req.CommandTimeoutSeconds > 86400) || req.ChatMaxRetries != nil && (*req.ChatMaxRetries < 0 || *req.ChatMaxRetries > 20) || req.ModelSyncIntervalHours != nil && (*req.ModelSyncIntervalHours < 1 || *req.ModelSyncIntervalHours > 720) || len(req.GlobalAgentsPaths) > 32 {
		return nil, ErrInvalidSystemInfo
	}
	if (req.ModelSyncURL != "" && validateModelSyncURL(req.ModelSyncURL) != nil) || (req.ProviderSyncURL != "" && validateModelSyncURL(req.ProviderSyncURL) != nil) {
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
		SetSystemPrompt(req.SystemPrompt).SetModelSyncEnabled(req.ModelSyncEnabled).
		SetDefaultModelID(req.DefaultModelID).SetTaskModelID(req.TaskModelID)
	if req.GlobalSystemPrompt != nil {
		update.SetGlobalSystemPrompt(*req.GlobalSystemPrompt)
	}
	if req.ModelSyncIntervalHours != nil {
		update.SetModelSyncIntervalHours(*req.ModelSyncIntervalHours)
	}
	if req.ModelSyncURL != "" {
		update.SetModelSyncURL(req.ModelSyncURL)
	}
	if req.ProviderSyncURL != "" {
		update.SetProviderSyncURL(req.ProviderSyncURL)
	}
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
	DefaultModelCatalogSyncer.Notify()
	return systemInfoResponse(row), nil
}

func systemInfoResponse(row *ent.KaguyaSystemInfo) *dtosystem.SystemInfoResp {
	return &dtosystem.SystemInfoResp{
		SystemInfoSaveReq: dtosystem.SystemInfoSaveReq{
			ContextCompactionPercent: &row.ContextCompactionPercent,
			GlobalSystemPrompt:       &row.GlobalSystemPrompt, SystemPrompt: row.SystemPrompt,
			AgentMaxSteps: &row.AgentMaxSteps, CommandTimeoutSeconds: &row.CommandTimeoutSeconds, ChatMaxRetries: &row.ChatMaxRetries, GlobalAgentsPaths: defaultAgentsPaths(row.GlobalAgentsPaths),
			ModelSyncEnabled: row.ModelSyncEnabled, ModelSyncURL: row.ModelSyncURL, ProviderSyncURL: row.ProviderSyncURL, ModelSyncIntervalHours: &row.ModelSyncIntervalHours,
			DefaultModelID: row.DefaultModelID, TaskModelID: row.TaskModelID,
		},
		ModelSyncCatalogCount: row.ModelCatalogCount, ProviderCatalogCount: row.ProviderCatalogCount,
		ModelSyncLastAttemptAt: row.ModelSyncLastAttemptAt,
		ModelSyncLastSuccessAt: row.ModelSyncLastSuccessAt, ModelSyncLastError: row.ModelSyncLastError,
	}
}

func validateModelSyncURL(raw string) error {
	if len(raw) > 2048 || strings.TrimSpace(raw) != raw {
		return ErrInvalidSystemInfo
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return ErrInvalidSystemInfo
	}
	return nil
}

// ChatSystemPrompt 将可编辑的基础提示词与附加提示词拼接。
func ChatSystemPrompt(base, custom string) string {
	base = strings.TrimSpace(base)
	custom = strings.TrimSpace(custom)
	if base == "" {
		return custom
	}
	if custom == "" {
		return base
	}
	return base + "\n\n" + custom
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
