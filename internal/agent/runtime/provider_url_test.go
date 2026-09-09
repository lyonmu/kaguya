package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/consts"
)

func TestProviderRequestURL(t *testing.T) {
	for _, protocol := range []consts.ProviderProtocol{consts.ProtocolOpenAIChat, consts.ProtocolOpenAIResponses, consts.ProtocolAnthropic} {
		endpoint := "/chat/completions"
		switch protocol {
		case consts.ProtocolOpenAIResponses:
			endpoint = "/responses"
		case consts.ProtocolAnthropic:
			endpoint = "/messages"
		}
		for _, path := range []string{"", "/", "/v1", "/v1/", "/v1" + endpoint, "/v1" + endpoint + "/", "/gateway/v1", "/gateway/v1" + endpoint} {
			t.Run(string(protocol)+path, func(t *testing.T) {
				want := "/v1" + endpoint
				if strings.HasPrefix(path, "/gateway") {
					want = "/gateway" + want
				}
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.URL.Path != want {
						t.Errorf("path = %q, want %q", r.URL.Path, want)
					}
					// 不重试的错误响应即可验证真实 SDK 的路径拼接。
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"test"}}`))
				}))
				defer server.Close()
				a, err := New(WithProvider(ProviderConfig{Protocol: protocol, BaseURL: server.URL + path, APIKey: "test", ModelID: "test"}))
				if err != nil {
					t.Fatal(err)
				}
				retries := 0
				_, err = a.Stream(context.Background(), fantasy.AgentStreamCall{Prompt: "hello", MaxRetries: &retries})
				if err == nil || calls != 1 {
					t.Fatalf("calls = %d, err = %v", calls, err)
				}
			})
		}
	}
}

func TestNormalizeProviderBaseURL(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"", ""},
		{" https://example.com:8443/ ", "https://example.com:8443/v1"},
		{"https://example.com/custom/", "https://example.com/custom/"},
		{"https://example.com/v2", "https://example.com/v2"},
		{"https://example.com/v1/responses?key=value", "https://example.com/v1?key=value"},
	} {
		got, err := normalizeProviderBaseURL(tt.input, consts.ProtocolOpenAIResponses)
		if err != nil || got != tt.want {
			t.Errorf("normalize(%q) = %q, %v; want %q", tt.input, got, err, tt.want)
		}
	}
	for _, input := range []string{"example.com", "/v1", "ftp://example.com", "https://", "https://%"} {
		if _, err := normalizeProviderBaseURL(input, consts.ProtocolOpenAIResponses); err == nil {
			t.Errorf("expected invalid URL error for %q", input)
		}
	}
}
