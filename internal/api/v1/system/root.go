package system

import "github.com/lyonmu/kaguya/internal/service/system"

type SystemApiV1Group struct{}

var (
	systemsvc = system.SystemSvc{}
)
