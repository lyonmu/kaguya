package system

import "errors"

type SystemSvc struct{}

var (
	ErrProviderNotFound  = errors.New("provider not found")
	ErrProviderDuplicate = errors.New("provider name already exists")
	ErrModelNotFound     = errors.New("model not found")
	ErrModelDuplicate    = errors.New("model ID already exists for provider")
)
