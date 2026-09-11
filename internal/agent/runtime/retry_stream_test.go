package agent

import (
	"errors"
	"testing"

	"charm.land/fantasy"
)

func TestClassifyTransientStreamError(t *testing.T) {
	tests := []struct {
		name      string
		message   string
		retryable bool
	}{
		{name: "overload code", message: `received error while streaming: {"type":"service_unavailable_error","code":"server_is_overloaded","message":"overloaded"}`, retryable: true},
		{name: "nested standard error", message: `received error while streaming: {"error":{"type":"rate_limit_error","message":"slow down"}}`, retryable: true},
		{name: "invalid request", message: `received error while streaming: {"type":"invalid_request_error","message":"bad input"}`},
		{name: "unstructured", message: "received error while streaming: not json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			original := errors.New(tc.message)
			got := classifyTransientStreamError(original)
			var providerErr *fantasy.ProviderError
			if errors.As(got, &providerErr) != tc.retryable {
				t.Fatalf("error=%T %v", got, got)
			}
			if tc.retryable && !providerErr.IsRetryable() {
				t.Fatalf("not retryable: %v", providerErr)
			}
			if !tc.retryable && got != original {
				t.Fatalf("non-transient error changed: %v", got)
			}
		})
	}
}
