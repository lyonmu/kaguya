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

func providerQuery(client *ent.Client) *ent.KaguyaProviderInfoQuery {
	return client.KaguyaProviderInfo.Query().Where(kaguyaproviderinfo.DeletedAtIsNil())
}

func providerQueryWithModels(client *ent.Client) *ent.KaguyaProviderInfoQuery {
	return providerQuery(client).WithModels(func(query *ent.KaguyaModelsInfoQuery) {
		query.Where(kaguyamodelsinfo.DeletedAtIsNil()).Order(kaguyamodelsinfo.ByModelName())
	})
}

// ProviderPage 分页查询提供商及其模型。
func (s *SystemSvc) ProviderPage(ctx context.Context, req *dtosystem.SystemProviderPageReq) (*dtosystem.SystemProviderListResp, error) {
	query := providerQueryWithModels(db.EntClient)
	if req.ProviderName != "" {
		query.Where(kaguyaproviderinfo.ProviderNameContains(req.ProviderName))
	}
	if req.APIProtocol != "" {
		query.Where(kaguyaproviderinfo.APIProtocolEQ(req.APIProtocol))
	}

	total, err := query.Count(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("query provider count failed: %v", err)
		return nil, err
	}
	rows, err := query.Offset((req.Page - 1) * req.PageSize).
		Limit(req.PageSize).
		Order(kaguyaproviderinfo.ByCreatedAt(sql.OrderDesc())).
		All(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("query provider page failed: %v", err)
		return nil, err
	}

	items := make([]*dtosystem.SystemProviderResp, 0, len(rows))
	for _, row := range rows {
		item := &dtosystem.SystemProviderResp{}
		item.LoadDb(row)
		items = append(items, item)
	}
	return &dtosystem.SystemProviderListResp{Total: total, Items: items, Page: req.Page, PageSize: req.PageSize}, nil
}

// ProviderDetail 查询提供商详情及其模型。
func (s *SystemSvc) ProviderDetail(ctx context.Context, id string) (*dtosystem.SystemProviderResp, error) {
	row, err := providerQueryWithModels(db.EntClient).Where(kaguyaproviderinfo.IDEQ(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			global.Logger.Sugar().Warnf("provider not found: id=%s", id)
			return nil, ErrProviderNotFound
		}
		global.Logger.Sugar().Errorf("query provider detail failed: id=%s, err=%v", id, err)
		return nil, err
	}
	resp := &dtosystem.SystemProviderResp{}
	resp.LoadDb(row)
	return resp, nil
}

// ProviderCreate 创建提供商。
func (s *SystemSvc) ProviderCreate(ctx context.Context, req *dtosystem.SystemProviderSaveReq) (*dtosystem.SystemProviderResp, error) {
	kind := req.ProviderType
	if kind == "" {
		kind = consts.ProviderTypeNormal
	}
	row, err := db.EntClient.KaguyaProviderInfo.Create().
		SetProviderType(kind).
		SetProviderName(req.ProviderName).
		SetAPIProtocol(req.APIProtocol).
		SetAPIKey(req.APIKey).
		SetBaseURL(req.BaseURL).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			global.Logger.Sugar().Warnf("provider name already exists: name=%s", req.ProviderName)
			return nil, ErrProviderDuplicate
		}
		global.Logger.Sugar().Errorf("create provider failed: name=%s, err=%v", req.ProviderName, err)
		return nil, err
	}
	global.Logger.Sugar().Infof("provider created: id=%s, name=%s", row.ID, row.ProviderName)
	resp := &dtosystem.SystemProviderResp{}
	resp.LoadDb(row)
	return resp, nil
}

// ProviderUpdate 修改提供商。
func (s *SystemSvc) ProviderUpdate(ctx context.Context, id string, req *dtosystem.SystemProviderSaveReq) (*dtosystem.SystemProviderResp, error) {
	kind := req.ProviderType
	if kind == "" {
		kind = consts.ProviderTypeNormal
	}
	row, err := db.EntClient.KaguyaProviderInfo.UpdateOneID(id).
		Where(kaguyaproviderinfo.DeletedAtIsNil()).
		SetProviderName(req.ProviderName).
		SetProviderType(kind).
		SetAPIProtocol(req.APIProtocol).
		SetAPIKey(req.APIKey).
		SetBaseURL(req.BaseURL).
		Save(ctx)
	if err != nil {
		switch {
		case ent.IsNotFound(err):
			global.Logger.Sugar().Warnf("provider not found for update: id=%s", id)
			return nil, ErrProviderNotFound
		case ent.IsConstraintError(err):
			global.Logger.Sugar().Warnf("provider name already exists: id=%s, name=%s", id, req.ProviderName)
			return nil, ErrProviderDuplicate
		default:
			global.Logger.Sugar().Errorf("update provider failed: id=%s, err=%v", id, err)
			return nil, err
		}
	}
	global.Logger.Sugar().Infof("provider updated: id=%s", id)
	resp := &dtosystem.SystemProviderResp{}
	resp.LoadDb(row)
	return resp, nil
}

// ProviderDelete 软删除提供商，并同步软删除其下所有模型。
func (s *SystemSvc) ProviderDelete(ctx context.Context, id string) error {
	tx, err := db.EntClient.Tx(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("start provider delete transaction failed: id=%s, err=%v", id, err)
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()

	exists, err := providerQuery(client).Where(kaguyaproviderinfo.IDEQ(id)).Exist(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("query provider before delete failed: id=%s, err=%v", id, err)
		return err
	}
	if !exists {
		global.Logger.Sugar().Warnf("provider not found for delete: id=%s", id)
		return ErrProviderNotFound
	}

	ids, err := client.KaguyaModelsInfo.Query().Where(kaguyamodelsinfo.ProviderIDEQ(id)).IDs(ctx)
	if err != nil {
		return err
	}
	if err := clearModelSelections(ctx, client, ids...); err != nil {
		return err
	}
	now := time.Now()
	if _, err = client.KaguyaModelsInfo.Update().
		Where(kaguyamodelsinfo.ProviderIDEQ(id), kaguyamodelsinfo.DeletedAtIsNil()).
		SetDeletedAt(now).
		ClearIsTask().
		Save(ctx); err != nil {
		global.Logger.Sugar().Errorf("delete provider models failed: id=%s, err=%v", id, err)
		return err
	}
	if _, err = client.KaguyaProviderInfo.UpdateOneID(id).
		Where(kaguyaproviderinfo.DeletedAtIsNil()).
		SetDeletedAt(now).
		Save(ctx); err != nil {
		global.Logger.Sugar().Errorf("delete provider failed: id=%s, err=%v", id, err)
		return err
	}
	if err = tx.Commit(); err != nil {
		global.Logger.Sugar().Errorf("commit provider delete failed: id=%s, err=%v", id, err)
		return err
	}
	global.Logger.Sugar().Infof("provider deleted: id=%s", id)
	return nil
}

// ProviderLabels 查询提供商下拉选项。
func (s *SystemSvc) ProviderLabels(ctx context.Context, req *dtosystem.SystemProviderLabelReq) ([]*dtosystem.SystemProviderLabelResp, error) {
	query := providerQuery(db.EntClient)
	if req.Keyword != "" {
		query.Where(kaguyaproviderinfo.ProviderNameContains(req.Keyword))
	}
	rows, err := query.Order(kaguyaproviderinfo.ByProviderName()).All(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("query provider labels failed: %v", err)
		return nil, err
	}
	items := make([]*dtosystem.SystemProviderLabelResp, 0, len(rows))
	for _, row := range rows {
		items = append(items, &dtosystem.SystemProviderLabelResp{Label: row.ProviderName, Value: row.ID})
	}
	return items, nil
}
