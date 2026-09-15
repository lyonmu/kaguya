// Package initialize 显式初始化启动资源和数据库基础数据，不使用 Go init() 隐式写库。
package initialize

import (
	"context"
	"fmt"

	"github.com/lyonmu/kaguya/internal/ent"
)

func Run(ctx context.Context, client *ent.Client) error {
	if client == nil {
		return fmt.Errorf("database client is required for initialization")
	}
	if err := Info(ctx, client); err != nil {
		return fmt.Errorf("initialize system info: %w", err)
	}
	if err := migrateCatalogSyncURLs(ctx, client); err != nil {
		return fmt.Errorf("migrate catalog sync URLs: %w", err)
	}
	if err := backfillModelRequestPaths(ctx, client); err != nil {
		return fmt.Errorf("backfill model request paths: %w", err)
	}
	return nil
}
