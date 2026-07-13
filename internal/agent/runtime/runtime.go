package runtime

import (
	"errors"

	"charm.land/fantasy"

	"github.com/lyonmu/kaguya/internal/agent/tools/files"
	"github.com/lyonmu/kaguya/internal/agent/usage"
)

type Config struct {
	Model        fantasy.LanguageModel
	SystemPrompt string
	Files        *files.SafeFS
	Recorder     usage.UsageRecorder
}

func New(cfg Config) (fantasy.Agent, error) {
	if cfg.Model == nil {
		return nil, errors.New("model is required")
	}
	if cfg.Files == nil {
		return nil, errors.New("file tools are required")
	}

	inner := fantasy.NewAgent(cfg.Model,
		fantasy.WithSystemPrompt(cfg.SystemPrompt),
		fantasy.WithTools(cfg.Files.Tools()...),
	)

	return usage.NewAgent(inner, cfg.Recorder,
		usage.WithProvider(cfg.Model.Provider()),
		usage.WithModel(cfg.Model.Model()),
		usage.WithConversationIDFunc(usage.ConversationIDFromContext),
		usage.WithMessageIDFunc(usage.MessageIDFromContext),
	), nil
}
