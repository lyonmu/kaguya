package memory

import (
	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/global"
	svcmemory "github.com/lyonmu/kaguya/internal/service/memory"
	projectsvc "github.com/lyonmu/kaguya/internal/service/project"
	"go.uber.org/zap"
)

type MemoryApiV1Group struct{}

// memorysvc 每次装配时捕获当前 client/logger，并注入复用项目路径校验、忽略规则
// 与大小限制的资料读取器，避免 memory 包反向依赖项目服务。
func memorysvc() *svcmemory.Service {
	return svcmemory.NewService(db.EntClient, global.Logger.With(zap.String("component", "memory"))).
		WithDocumentReader(&projectsvc.ProjectSvc{})
}
