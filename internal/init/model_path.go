package initialize

import (
	"context"

	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamodelsinfo"
	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
)

// backfillModelRequestPaths 只回填空路径，已配置的路径不会被覆盖。
// 升级前的最终地址是 provider.BaseURL + 协议默认后缀，回填同一默认值可保持升级后请求地址不变。
func backfillModelRequestPaths(ctx context.Context, client *ent.Client) error {
	rows, err := client.KaguyaModelsInfo.Query().
		Where(kaguyamodelsinfo.RequestPathEQ("")).
		Select(kaguyamodelsinfo.FieldID, kaguyamodelsinfo.FieldAPIProtocol).
		All(ctx)
	if err != nil {
		return err
	}
	filled := 0
	for _, row := range rows {
		path := row.APIProtocol.DefaultRequestPath()
		if path == "" {
			if global.Logger != nil {
				global.Logger.Warn("skip request path backfill for unsupported protocol",
					zap.String("model_id", row.ID), zap.String("protocol", string(row.APIProtocol)))
			}
			continue
		}
		if err := client.KaguyaModelsInfo.UpdateOneID(row.ID).SetRequestPath(path).Exec(ctx); err != nil {
			return err
		}
		filled++
	}
	if filled > 0 && global.Logger != nil {
		global.Logger.Sugar().Infof("model request paths backfilled: %d", filled)
	}
	return nil
}
