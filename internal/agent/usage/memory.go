package usage

import (
	"context"
	"sync"
)

type MemoryRecorder struct {
	mu    sync.Mutex
	items []TurnUsage
}

func NewMemoryRecorder() *MemoryRecorder {
	return &MemoryRecorder{}
}

func (r *MemoryRecorder) RecordUsage(ctx context.Context, usage TurnUsage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.items = append(r.items, usage)
	return nil
}

func (r *MemoryRecorder) Items() []TurnUsage {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]TurnUsage, len(r.items))
	copy(out, r.items)
	return out
}
