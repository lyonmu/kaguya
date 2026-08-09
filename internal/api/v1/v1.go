package v1

import (
	"github.com/lyonmu/kaguya/internal/api/v1/chat"
	"github.com/lyonmu/kaguya/internal/api/v1/system"
)

type ApiV1Group struct {
	chat.ChatApiV1Group
	system.SystemApiV1Group
}
