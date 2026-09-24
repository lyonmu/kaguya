package memory

import (
	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/global"
	svcmemory "github.com/lyonmu/kaguya/internal/service/memory"
	"go.uber.org/zap"
)

type MemoryApiV1Group struct{}

// memorysvc 每次装配时捕获当前 client/logger，避免固定可变全局状态。
func memorysvc() *svcmemory.Service {
	return svcmemory.NewService(db.EntClient, global.Logger.With(zap.String("component", "memory")))
}
