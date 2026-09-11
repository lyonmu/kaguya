package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"charm.land/fantasy"
)

const streamErrorPrefix = "received error while streaming:"

var transientStreamErrors = map[string]bool{
	"api_error":                 true,
	"internal_error":            true,
	"overloaded_error":          true,
	"rate_limit_error":          true,
	"server_error":              true,
	"server_is_overloaded":      true,
	"service_unavailable_error": true,
}

type streamErrorEnvelope struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Error   *struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// retryableStreamModel classifies transient errors carried inside an already
// successful SSE response. Fantasy v0.33 can retry HTTP 5xx responses, but the
// provider SDK exposes these in-band errors without an HTTP status.
type retryableStreamModel struct{ fantasy.LanguageModel }

func withRetryableStreamErrors(model fantasy.LanguageModel) fantasy.LanguageModel {
	return retryableStreamModel{LanguageModel: model}
}

func (m retryableStreamModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	stream, err := m.LanguageModel.Stream(ctx, call)
	if err != nil {
		return nil, classifyTransientStreamError(err)
	}
	return func(yield func(fantasy.StreamPart) bool) {
		stream(func(part fantasy.StreamPart) bool {
			if part.Type == fantasy.StreamPartTypeError {
				part.Error = classifyTransientStreamError(part.Error)
			}
			return yield(part)
		})
	}, nil
}

func classifyTransientStreamError(err error) error {
	if err == nil {
		return nil
	}
	var providerErr *fantasy.ProviderError
	if errors.As(err, &providerErr) {
		return err
	}
	_, payload, ok := strings.Cut(err.Error(), streamErrorPrefix)
	if !ok {
		return err
	}
	payload = strings.TrimSpace(payload)
	var envelope streamErrorEnvelope
	if json.Unmarshal([]byte(payload), &envelope) != nil {
		return err
	}
	if envelope.Error != nil {
		envelope.Type = firstNonEmpty(envelope.Error.Type, envelope.Type)
		envelope.Code = firstNonEmpty(envelope.Error.Code, envelope.Code)
		envelope.Message = firstNonEmpty(envelope.Error.Message, envelope.Message)
	}
	errType := strings.ToLower(firstNonEmpty(envelope.Type, envelope.Code))
	if !transientStreamErrors[errType] && !transientStreamErrors[strings.ToLower(envelope.Code)] {
		return err
	}
	message := firstNonEmpty(envelope.Message, payload)
	return &fantasy.ProviderError{
		Title:        "provider temporarily unavailable",
		Message:      message,
		Cause:        err,
		StatusCode:   http.StatusServiceUnavailable,
		ResponseBody: []byte(payload),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
