package system

import (
	"context"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamodelsinfo"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaproviderinfo"
	"github.com/lyonmu/kaguya/internal/global"
)

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

// ModelCreate 只创建模型，默认/任务模型统一由系统配置管理。
func (s *SystemSvc) ModelCreate(ctx context.Context, req *dtosystem.SystemModelSaveReq) (*dtosystem.SystemModelResp, error) {
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
		SetIsDefault(consts.IsFalse). // 旧列不再参与模型选择，新模型不写选择标记。
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

// ModelUpdate 修改模型信息，不改变系统配置中的模型选择。
func (s *SystemSvc) ModelUpdate(ctx context.Context, id string, req *dtosystem.SystemModelSaveReq) (*dtosystem.SystemModelResp, error) {
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
		ClearIsTask().
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
			Label: row.ModelName, Value: row.ID, ProviderID: row.ProviderID, ModelID: row.ModelID, ProviderName: row.Edges.Provider.ProviderName,
		})
	}
	return items, nil
}
