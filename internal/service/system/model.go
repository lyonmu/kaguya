package system

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/fantasy"
	"entgo.io/ent/dialect/sql"
	agentruntime "github.com/lyonmu/kaguya/internal/agent/runtime"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamodelsinfo"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaproviderinfo"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/internal/secret"
)

const (
	// modelTestPrompt 是连接测试发送的唯一用户消息。
	modelTestPrompt = "Hi!"
	// modelTestTimeout 限制一次连接测试的总时长，避免提供商无响应时长期占用请求。
	modelTestTimeout = 60 * time.Second
)

// ErrModelTest 表示用待保存的模型配置发起真实调用失败；面向用户的文案由错误原文组成，
// 便于直接看出是鉴权、地址还是模型标识的问题。
var ErrModelTest = errors.New("模型测试失败")

func modelQuery(client *ent.Client) *ent.KaguyaModelsInfoQuery {
	return client.KaguyaModelsInfo.Query().
		Where(
			kaguyamodelsinfo.DeletedAtIsNil(),
			kaguyamodelsinfo.HasProviderWith(kaguyaproviderinfo.DeletedAtIsNil()),
		).
		WithProvider(func(query *ent.KaguyaProviderInfoQuery) {
			query.Where(kaguyaproviderinfo.DeletedAtIsNil())
		})
}

// ModelPage 分页查询模型。
func (s *SystemSvc) ModelPage(ctx context.Context, req *dtosystem.SystemModelPageReq) (*dtosystem.SystemModelListResp, error) {
	query := modelQuery(db.EntClient)
	if req.ProviderID != "" {
		query.Where(kaguyamodelsinfo.ProviderIDEQ(req.ProviderID))
	}
	if req.Keyword != "" {
		query.Where(kaguyamodelsinfo.Or(
			kaguyamodelsinfo.ModelNameContains(req.Keyword),
			kaguyamodelsinfo.ModelIDContains(req.Keyword),
		))
	}

	total, err := query.Count(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("query model count failed: %v", err)
		return nil, err
	}
	rows, err := query.Offset((req.Page - 1) * req.PageSize).
		Limit(req.PageSize).
		Order(kaguyamodelsinfo.ByCreatedAt(sql.OrderDesc())).
		All(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("query model page failed: %v", err)
		return nil, err
	}
	items := make([]*dtosystem.SystemModelResp, 0, len(rows))
	for _, row := range rows {
		item := &dtosystem.SystemModelResp{}
		item.LoadDb(row)
		items = append(items, item)
	}
	return &dtosystem.SystemModelListResp{Total: total, Items: items, Page: req.Page, PageSize: req.PageSize}, nil
}

// ModelDetail 查询模型详情。
func (s *SystemSvc) ModelDetail(ctx context.Context, id string) (*dtosystem.SystemModelResp, error) {
	row, err := modelQuery(db.EntClient).Where(kaguyamodelsinfo.IDEQ(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			global.Logger.Sugar().Warnf("model not found: id=%s", id)
			return nil, ErrModelNotFound
		}
		global.Logger.Sugar().Errorf("query model detail failed: id=%s, err=%v", id, err)
		return nil, err
	}
	resp := &dtosystem.SystemModelResp{}
	resp.LoadDb(row)
	return resp, nil
}

// validateRequestPath 只做拼接所需的最低校验：以 / 开头且不含空白、查询或片段。
func validateRequestPath(raw string) error {
	if raw == "" || raw[0] != '/' || strings.ContainsAny(raw, " \t\r\n?#") {
		return ErrModelRequestPath
	}
	return nil
}

// ModelCreate 只创建模型，默认/任务模型统一由系统配置管理。
func (s *SystemSvc) ModelCreate(ctx context.Context, req *dtosystem.SystemModelSaveReq) (*dtosystem.SystemModelResp, error) {
	if err := validateRequestPath(req.RequestPath); err != nil {
		return nil, err
	}
	tx, err := db.EntClient.Tx(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("start model create transaction failed: err=%v", err)
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()

	provider, err := providerQuery(client).Where(kaguyaproviderinfo.IDEQ(req.ProviderID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			global.Logger.Sugar().Warnf("provider not found for model create: provider_id=%s", req.ProviderID)
			return nil, ErrProviderNotFound
		}
		global.Logger.Sugar().Errorf("query provider before creating model failed: provider_id=%s, err=%v", req.ProviderID, err)
		return nil, err
	}
	row, err := client.KaguyaModelsInfo.Create().
		SetProviderID(req.ProviderID).
		SetModelName(req.ModelName).
		SetModelID(req.ModelID).
		SetAPIProtocol(req.APIProtocol).
		SetRequestPath(req.RequestPath).
		SetReasoningEnabled(req.ReasoningEnabled).
		SetReasoningEffort(req.ReasoningEffort).
		SetTokenContextWindow(req.TokenContextWindow).
		SetTokenMaxOutputTokens(req.TokenMaxOutputTokens).
		SetCapabilityToolUse(req.CapabilityToolUse).
		SetCapabilityVision(req.CapabilityVision).
		SetCapabilityStructuredOutput(req.CapabilityStructuredOutput).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			global.Logger.Sugar().Warnf("model ID already exists: provider_id=%s, model_id=%s", req.ProviderID, req.ModelID)
			return nil, ErrModelDuplicate
		}
		global.Logger.Sugar().Errorf("create model failed: provider_id=%s, model_id=%s, err=%v", req.ProviderID, req.ModelID, err)
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		global.Logger.Sugar().Errorf("commit model create failed: model_id=%s, err=%v", req.ModelID, err)
		return nil, err
	}
	global.Logger.Sugar().Infof("model created: id=%s, provider_id=%s", row.ID, req.ProviderID)
	resp := &dtosystem.SystemModelResp{}
	resp.LoadDb(row)
	resp.ProviderName = provider.ProviderName
	return resp, nil
}

// ModelTest 用待保存的模型配置组装一次真实调用（只发送 "Hi!"），验证提供商与模型是否可用。
// 配置不落库，也不计入用量：新增与修改都先用请求中的字段测试。
func (s *SystemSvc) ModelTest(ctx context.Context, req *dtosystem.SystemModelSaveReq) (*dtosystem.SystemModelTestResp, error) {
	if err := validateRequestPath(req.RequestPath); err != nil {
		return nil, err
	}
	provider, err := providerQuery(db.EntClient).Where(kaguyaproviderinfo.IDEQ(req.ProviderID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			global.Logger.Sugar().Warnf("provider not found for model test: provider_id=%s", req.ProviderID)
			return nil, ErrProviderNotFound
		}
		global.Logger.Sugar().Errorf("query provider before model test failed: provider_id=%s, err=%v", req.ProviderID, err)
		return nil, err
	}
	apiKey, err := secret.Decrypt(provider.APIKey)
	if err != nil {
		global.Logger.Sugar().Errorf("decrypt provider api key for model test failed: provider_id=%s, err=%v", provider.ID, err)
		return nil, ErrProviderSecret
	}
	// 测试不属于任何会话，生成一次性 ID 透传，保持与真实对话一致的请求头。
	id, err := global.Id.GenID()
	if err != nil {
		global.Logger.Sugar().Errorf("generate model test conversation id failed: provider_id=%s, err=%v", req.ProviderID, err)
		return nil, err
	}

	testCtx, cancel := context.WithTimeout(ctx, modelTestTimeout)
	defer cancel()
	ag, err := agentruntime.New(agentruntime.WithProvider(agentruntime.ProviderConfig{
		Name: provider.ProviderName, Type: provider.ProviderType, Protocol: consts.ProviderProtocol(req.APIProtocol),
		ReasoningEnabled: req.ReasoningEnabled, ReasoningEffort: req.ReasoningEffort,
		BaseURL: provider.BaseURL, RequestPath: req.RequestPath, APIKey: apiKey, ModelID: req.ModelID,
		ConversationID: fmt.Sprintf("%d", id),
	}))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrModelTest, err)
	}

	// 配置错误要尽快反馈：不做重试，一次失败直接返回提供商返回的原因。
	maxRetries := 0
	startedAt := time.Now()
	result, err := ag.Generate(testCtx, fantasy.AgentCall{Prompt: modelTestPrompt, MaxRetries: &maxRetries})
	if err != nil {
		global.Logger.Sugar().Warnf("model test call failed: provider_id=%s, model_id=%s, err=%v", req.ProviderID, req.ModelID, err)
		return nil, fmt.Errorf("%w: %s", ErrModelTest, err)
	}
	resp := &dtosystem.SystemModelTestResp{DurationMS: time.Since(startedAt).Milliseconds()}
	if result != nil {
		resp.Reply = strings.TrimSpace(result.Response.Content.Text())
	}
	global.Logger.Sugar().Infof("model test succeeded: provider_id=%s, model_id=%s", req.ProviderID, req.ModelID)
	return resp, nil
}

// ModelUpdate 修改模型信息，不改变系统配置中的模型选择。
func (s *SystemSvc) ModelUpdate(ctx context.Context, id string, req *dtosystem.SystemModelSaveReq) (*dtosystem.SystemModelResp, error) {
	if err := validateRequestPath(req.RequestPath); err != nil {
		return nil, err
	}
	tx, err := db.EntClient.Tx(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("start model update transaction failed: id=%s, err=%v", id, err)
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()

	modelExists, err := client.KaguyaModelsInfo.Query().Where(kaguyamodelsinfo.IDEQ(id), kaguyamodelsinfo.DeletedAtIsNil()).Exist(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("query model before update failed: id=%s, err=%v", id, err)
		return nil, err
	}
	if !modelExists {
		global.Logger.Sugar().Warnf("model not found for update: id=%s", id)
		return nil, ErrModelNotFound
	}
	provider, err := providerQuery(client).Where(kaguyaproviderinfo.IDEQ(req.ProviderID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			global.Logger.Sugar().Warnf("provider not found for model update: provider_id=%s", req.ProviderID)
			return nil, ErrProviderNotFound
		}
		global.Logger.Sugar().Errorf("query provider before updating model failed: provider_id=%s, err=%v", req.ProviderID, err)
		return nil, err
	}
	row, err := client.KaguyaModelsInfo.UpdateOneID(id).
		Where(kaguyamodelsinfo.DeletedAtIsNil()).
		SetProviderID(req.ProviderID).
		SetModelName(req.ModelName).
		SetModelID(req.ModelID).
		SetAPIProtocol(req.APIProtocol).
		SetRequestPath(req.RequestPath).
		SetReasoningEnabled(req.ReasoningEnabled).
		SetReasoningEffort(req.ReasoningEffort).
		SetTokenContextWindow(req.TokenContextWindow).
		SetTokenMaxOutputTokens(req.TokenMaxOutputTokens).
		SetCapabilityToolUse(req.CapabilityToolUse).
		SetCapabilityVision(req.CapabilityVision).
		SetCapabilityStructuredOutput(req.CapabilityStructuredOutput).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			global.Logger.Sugar().Warnf("model ID already exists: id=%s, provider_id=%s, model_id=%s", id, req.ProviderID, req.ModelID)
			return nil, ErrModelDuplicate
		}
		global.Logger.Sugar().Errorf("update model failed: id=%s, err=%v", id, err)
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		global.Logger.Sugar().Errorf("commit model update failed: id=%s, err=%v", id, err)
		return nil, err
	}
	global.Logger.Sugar().Infof("model updated: id=%s", id)
	resp := &dtosystem.SystemModelResp{}
	resp.LoadDb(row)
	resp.ProviderName = provider.ProviderName
	return resp, nil
}

// ModelDelete 软删除模型。
func (s *SystemSvc) ModelDelete(ctx context.Context, id string) error {
	tx, err := db.EntClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := tx.KaguyaModelsInfo.UpdateOneID(id).
		Where(kaguyamodelsinfo.DeletedAtIsNil()).
		SetDeletedAt(time.Now()).
		Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			global.Logger.Sugar().Warnf("model not found for delete: id=%s", id)
			return ErrModelNotFound
		}
		global.Logger.Sugar().Errorf("delete model failed: id=%s, err=%v", id, err)
		return err
	}
	if err := clearModelSelections(ctx, tx.Client(), id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	global.Logger.Sugar().Infof("model deleted: id=%s, provider_id=%s", row.ID, row.ProviderID)
	return nil
}

// ModelLabels 查询模型下拉选项，可按提供商过滤。
func (s *SystemSvc) ModelLabels(ctx context.Context, req *dtosystem.SystemModelLabelReq) ([]*dtosystem.SystemModelLabelResp, error) {
	info, err := db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil {
		global.Logger.Sugar().Errorf("query system config for model labels failed: %v", err)
		return nil, err
	}
	query := modelQuery(db.EntClient)
	if req.ProviderID != "" {
		query.Where(kaguyamodelsinfo.ProviderIDEQ(req.ProviderID))
	}
	if req.Keyword != "" {
		query.Where(kaguyamodelsinfo.Or(
			kaguyamodelsinfo.ModelNameContains(req.Keyword),
			kaguyamodelsinfo.ModelIDContains(req.Keyword),
		))
	}
	rows, err := query.Order(kaguyamodelsinfo.ByModelName()).All(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("query model labels failed: %v", err)
		return nil, err
	}
	items := make([]*dtosystem.SystemModelLabelResp, 0, len(rows))
	for _, row := range rows {
		items = append(items, &dtosystem.SystemModelLabelResp{
			Label: row.ModelName, Value: row.ID, ProviderID: row.ProviderID, ModelID: row.ModelID,
			ProviderName: row.Edges.Provider.ProviderName, IsDefault: row.ID == info.DefaultModelID,
		})
	}
	return items, nil
}
