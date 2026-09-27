package system

import (
	"context"
	"errors"

	agentruntime "github.com/lyonmu/kaguya/internal/agent/runtime"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamodelsinfo"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaproviderinfo"
	"github.com/lyonmu/kaguya/internal/ent/kaguyasysteminfo"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/internal/secret"
)

// ErrTaskModelNotConfigured 表示任务模型未配置或其模型/提供商已删除。
var ErrTaskModelNotConfigured = errors.New("task model is not configured")

// TaskModel 是一次后台任务执行尝试开始时形成的不可变调用快照。
// API Key 只存在于 Config 中的内存调用配置，不写入任务表或日志。
type TaskModel struct {
	Config               agentruntime.ProviderConfig
	ModelRecordID        string // 本地模型记录 ID
	ProviderID           string // 本地提供商记录 ID
	UpstreamModelID      string // 调用 API 时使用的上游模型标识
	TokenContextWindow   int
	TokenMaxOutputTokens int
}

// ResolveTaskModel 解析全局后台任务模型及其提供商的调用配置。
// 标题生成与 Memory 编译共用；从传入的 client 读取单例系统配置与模型，
// 后台任务不依赖可变全局 client。未配置或已删除时返回 ErrTaskModelNotConfigured，
// 密钥不可用时返回 ErrProviderSecret，绝不悄悄回退到聊天模型。
func ResolveTaskModel(ctx context.Context, client *ent.Client, conversationID string) (*TaskModel, error) {
	info, err := client.KaguyaSystemInfo.Query().Where(kaguyasysteminfo.IDEQ(consts.SystemInfoID)).
		Select(kaguyasysteminfo.FieldTaskModelID).Only(ctx)
	if err != nil {
		return nil, err
	}
	if info.TaskModelID == "" {
		return nil, ErrTaskModelNotConfigured
	}
	model, err := client.KaguyaModelsInfo.Query().Where(
		kaguyamodelsinfo.IDEQ(info.TaskModelID), kaguyamodelsinfo.DeletedAtIsNil(),
		kaguyamodelsinfo.HasProviderWith(kaguyaproviderinfo.DeletedAtIsNil()),
	).WithProvider().Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrTaskModelNotConfigured
	}
	if err != nil {
		return nil, err
	}
	provider := model.Edges.Provider
	if provider == nil {
		return nil, ErrTaskModelNotConfigured
	}
	apiKey, err := secret.Decrypt(provider.APIKey)
	if err != nil {
		// 不记录密钥材料；任务层会转为 blocked 等待用户修复。
		global.Logger.Sugar().Warnf("decrypt task model api key failed: conversation_id=%s provider_id=%s err=%v", conversationID, provider.ID, err)
		return nil, ErrProviderSecret
	}
	return &TaskModel{
		Config: agentruntime.ProviderConfig{
			ProviderID: provider.ID, ModelRecordID: model.ID, ModelName: model.ModelName,
			Name: provider.ProviderName, Type: provider.ProviderType, Protocol: consts.ProviderProtocol(model.APIProtocol),
			ReasoningEnabled: model.ReasoningEnabled, ReasoningEffort: model.ReasoningEffort,
			BaseURL: provider.BaseURL, RequestPath: model.RequestPath, APIKey: apiKey, ModelID: model.ModelID,
			ConversationID: conversationID,
		},
		ModelRecordID:        model.ID,
		ProviderID:           provider.ID,
		UpstreamModelID:      model.ModelID,
		TokenContextWindow:   model.TokenContextWindow,
		TokenMaxOutputTokens: model.TokenMaxOutputTokens,
	}, nil
}
