package webtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
)

const (
	deepSeekToolsURL = "https://api.deepseek.com/v1/tools/"
	maxResponseSize  = 1024 * 1024
)

var (
	errInvalidResponse = errors.New("DeepSeek returned an invalid web tool response")
	errPageUnavailable = errors.New("DeepSeek could not retrieve the requested page")
	errTimeout         = errors.New("DeepSeek web tool request timed out")
)

type client struct {
	apiKey string
	http   *http.Client
}

func (c client) post(ctx context.Context, operation string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deepSeekToolsURL+operation, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if os.IsTimeout(err) {
			return errTimeout
		}
		return errors.New("DeepSeek web tool request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		if os.IsTimeout(err) {
			return errTimeout
		}
		return errors.New("could not read DeepSeek web tool response")
	}
	if len(data) > maxResponseSize {
		return errors.New("DeepSeek web tool response exceeded the size limit")
	}
	if err := json.Unmarshal(data, output); err != nil {
		return errInvalidResponse
	}
	return nil
}

func responseError(status int) error {
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return errors.New("DeepSeek rejected the web tool request parameters")
	case http.StatusUnauthorized:
		return errors.New("DeepSeek web tool API key is invalid")
	case http.StatusPaymentRequired:
		return errors.New("DeepSeek web tool account has insufficient balance")
	case http.StatusForbidden:
		return errors.New("DeepSeek denied the web tool request")
	case http.StatusNotFound:
		return errPageUnavailable
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return errTimeout
	case http.StatusTooManyRequests:
		return errors.New("DeepSeek web tool rate limit was exceeded")
	default:
		return errors.New("DeepSeek web tool service is unavailable")
	}
}
