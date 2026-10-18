package system

import "github.com/lyonmu/kaguya/internal/service/system"

type SystemApiV1Group struct{}

var (
	systemsvc = &system.SystemSvc{}
)

// UseRuntimeService 只在启动、注册 Handler 前接线；运行中不得替换。
func UseRuntimeService(svc *system.SystemSvc) { systemsvc = svc }
