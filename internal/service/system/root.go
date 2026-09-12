package system

import "errors"

type SystemSvc struct{}

var (
	ErrProviderNotFound  = errors.New("provider not found")
	ErrProviderDuplicate = errors.New("provider name already exists")
	ErrProviderSecret    = errors.New("provider API key could not be encrypted or decrypted")
	ErrModelNotFound     = errors.New("model not found")
	ErrModelDuplicate    = errors.New("model ID already exists for provider")
)
