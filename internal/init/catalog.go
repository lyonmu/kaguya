package initialize

import (
	"context"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyasysteminfo"
)

// migrateCatalogSyncURLs 把单一目录地址拆成提供商目录（api.json）与模型目录（models.json）。
// 旧库的 model_sync_url 原指 api.json，含义已改为 models.json，需要一次性改写；
// 只有仍为旧默认地址时才改写，自定义镜像留给用户自行调整。
func migrateCatalogSyncURLs(ctx context.Context, client *ent.Client) error {
	row, err := client.KaguyaSystemInfo.Query().
		Where(kaguyasysteminfo.IDEQ(consts.SystemInfoID)).
		Select(kaguyasysteminfo.FieldID, kaguyasysteminfo.FieldProviderSyncURL, kaguyasysteminfo.FieldModelSyncURL).
		Only(ctx)
	if err != nil {
		return err
	}
	update := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID)
	changed := false
	if row.ProviderSyncURL == "" {
		// 老库没有提供商目录地址：原 model_sync_url 就是 api.json 地址。
		providerURL := row.ModelSyncURL
		if providerURL == "" {
			providerURL = consts.DefaultProviderCatalogURL
		}
		update.SetProviderSyncURL(providerURL)
		changed = true
	}
	if row.ModelSyncURL == "" || row.ModelSyncURL == consts.DefaultProviderCatalogURL {
		update.SetModelSyncURL(consts.DefaultModelCatalogURL)
		changed = true
	}
	if !changed {
		return nil
	}
	return update.Exec(ctx)
}
