package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"charm.land/fantasy"
)

// idleTestModel 按固定间隔连续产出片段，用于验证空闲看门狗只统计相邻片段间隔。
type idleTestModel struct {
	fantasy.LanguageModel
	parts    int
	interval time.Duration
}

func (m idleTestModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	return func(yield func(fantasy.StreamPart) bool) {
		for i := 0; i < m.parts; i++ {
			if i > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(m.interval):
				}
			}
			if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: "x"}) {
				return
			}
		}
	}, nil
}

func collectParts(stream fantasy.StreamResponse) []fantasy.StreamPart {
	var parts []fantasy.StreamPart
	stream(func(part fantasy.StreamPart) bool {
		parts = append(parts, part)
		return true
	})
	return parts
}

func TestIdleStreamTimeoutCancelsSilentProvider(t *testing.T) {
	model := withIdleStreamTimeout(idleTestModel{parts: 2, interval: time.Second}, 20*time.Millisecond)
	stream, err := model.Stream(context.Background(), fantasy.Call{})
	if err != nil {
		t.Fatal(err)
	}
	parts := collectParts(stream)
	if len(parts) != 2 || parts[0].Type != fantasy.StreamPartTypeTextDelta || parts[1].Type != fantasy.StreamPartTypeError {
		t.Fatalf("parts=%+v", parts)
	}
	var providerErr *fantasy.ProviderError
	if !errors.As(parts[1].Error, &providerErr) || !providerErr.IsRetryable() {
		t.Fatalf("idle error is not retryable: %v", parts[1].Error)
	}
}

func TestIdleStreamTimeoutKeepsActiveStream(t *testing.T) {
	model := withIdleStreamTimeout(idleTestModel{parts: 6, interval: 5 * time.Millisecond}, 100*time.Millisecond)
	stream, err := model.Stream(context.Background(), fantasy.Call{})
	if err != nil {
		t.Fatal(err)
	}
	parts := collectParts(stream)
	if len(parts) != 6 {
		t.Fatalf("parts=%d", len(parts))
	}
	for _, part := range parts {
		if part.Type == fantasy.StreamPartTypeError {
			t.Fatalf("active stream reported idle timeout: %v", part.Error)
		}
	}
}

func TestIdleStreamTimeoutIgnoresParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	model := withIdleStreamTimeout(idleTestModel{parts: 2, interval: time.Second}, time.Second)
	stream, err := model.Stream(ctx, fantasy.Call{})
	if err != nil {
		t.Fatal(err)
	}
	var parts []fantasy.StreamPart
	stream(func(part fantasy.StreamPart) bool {
		parts = append(parts, part)
		cancel()
		return true
	})
	if len(parts) != 1 {
		t.Fatalf("parts=%+v", parts)
	}
}
