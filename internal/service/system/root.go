package system

import (
	"context"
	"errors"
)

type SystemSvc struct{ mcpRoot context.Context }

// NewSystemSvc 在启动时绑定本实例 root；MCP 不从可替换的 Default 查找生命周期。
func NewSystemSvc(root context.Context) *SystemSvc { return &SystemSvc{mcpRoot: root} }

func (s *SystemSvc) mcpRequestContext(request context.Context) (context.Context, func(), error) {
	if s.mcpRoot == nil {
		return nil, nil, ErrMCPLifecycle
	}
	ctx, cancel := context.WithCancel(request)
	stop := context.AfterFunc(s.mcpRoot, cancel)
	if err := s.mcpRoot.Err(); err != nil {
		cancel()
		stop()
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		cancel()
		stop()
		return nil, nil, err
	}
	return ctx, func() { stop(); cancel() }, nil
}

var (
	ErrProviderNotFound  = errors.New("provider not found")
	ErrProviderDuplicate = errors.New("provider name already exists")
	ErrProviderSecret    = errors.New("provider API key could not be encrypted or decrypted")
	ErrModelNotFound     = errors.New("model not found")
	ErrModelDuplicate    = errors.New("model ID already exists for provider")
	ErrModelRequestPath  = errors.New("model request path must start with / and contain no whitespace, query or fragment")
)
