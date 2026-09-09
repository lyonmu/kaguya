// Package initialize 在数据库迁移完成后显式初始化基础数据，不使用 Go init() 隐式写库。
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
	return nil
}
