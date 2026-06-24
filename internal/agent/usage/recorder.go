package usage

import "context"

type UsageRecorder interface {
	RecordUsage(ctx context.Context, usage TurnUsage) error
}
