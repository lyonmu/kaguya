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
// successful SSE response, including gateway-specific error types and codes.
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
	var payload []byte
	if errors.As(err, &providerErr) {
		if providerErr.StatusCode != 0 || providerErr.IsRetryable() {
			return err
		}
		// Fantasy exposes in-band errors as a ProviderError with no HTTP status.
		payload = providerErr.ResponseBody
	} else {
		_, body, ok := strings.Cut(err.Error(), streamErrorPrefix)
		if !ok {
			return err
		}
		payload = []byte(strings.TrimSpace(body))
	}
	var envelope streamErrorEnvelope
	if json.Unmarshal(payload, &envelope) != nil {
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
	if providerErr != nil {
		classified := *providerErr
		classified.TransientError = true
		classified.Cause = err
		return &classified
	}
	message := firstNonEmpty(envelope.Message, string(payload))
	return &fantasy.ProviderError{
		Title:        "provider temporarily unavailable",
		Message:      message,
		Cause:        err,
		StatusCode:   http.StatusServiceUnavailable,
		ResponseBody: payload,
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
