package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/consts"
)

// 使用真实 SDK 验证最终 HTTP 请求：BaseURL 与模型 request_path 拼接成请求地址。
// 路径以协议端点结尾时，SDK 拼出的地址与真实地址一致，错误信息里也是真实地址；
// 其他路径由 HTTP 客户端改写，请求地址同样正确。
func TestProviderRequestURL(t *testing.T) {
	tests := []struct {
		name     string
		protocol consts.ProviderProtocol
		base     string
		path     string
		want     string
	}{
		{name: "chat-root", protocol: consts.ProtocolOpenAIChat, base: "", path: "/chat/completions", want: "/chat/completions"},
		{name: "chat-versioned", protocol: consts.ProtocolOpenAIChat, base: "/v1", path: "/chat/completions", want: "/v1/chat/completions"},
		{name: "chat-gateway", protocol: consts.ProtocolOpenAIChat, base: "/gateway//", path: "v1/chat/completions", want: "/gateway/v1/chat/completions"},
		{name: "responses", protocol: consts.ProtocolOpenAIResponses, base: "", path: "/responses", want: "/responses"},
		{name: "anthropic-plain", protocol: consts.ProtocolAnthropic, base: "", path: "/messages", want: "/messages"},
		{name: "anthropic-versioned", protocol: consts.ProtocolAnthropic, base: "/v1", path: "/messages", want: "/v1/messages"},
		{name: "anthropic-prefixed", protocol: consts.ProtocolAnthropic, base: "", path: "/anthropic/v1/messages", want: "/anthropic/v1/messages"},
		{name: "chat-custom", protocol: consts.ProtocolOpenAIChat, base: "/gateway", path: "/v1/custom", want: "/gateway/v1/custom"},
	}
	for _, tt := range tests {
		t.Run(string(tt.protocol)+tt.want, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.RequestURI != tt.want {
					t.Errorf("URI = %q, want %q", r.RequestURI, tt.want)
				}
				if _, ok := r.Header["User-Agent"]; ok {
					t.Errorf("unexpected User-Agent=%q", r.Header.Get("User-Agent"))
				}
				if r.Method != http.MethodPost {
					t.Errorf("method = %s", r.Method)
				}
				if r.Header.Get("X-Conversation-ID") != "123" || r.Header.Get("x-opencode-session") != "123" {
					t.Error("missing session headers")
				}
				if tt.protocol == consts.ProtocolAnthropic {
					if r.Header.Get("x-api-key") != "test" {
						t.Error("missing API key")
					}
				} else if r.Header.Get("Authorization") != "Bearer test" {
					t.Error("missing authorization")
				}
				var body struct {
					Model  string `json:"model"`
					Stream bool   `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Model != "test" || body.Stream != (calls == 1) {
					t.Errorf("request body = %+v", body)
				}
				// 不重试的错误响应即可验证真实请求路径、请求体与请求头。
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"test"}}`))
			}))
			defer server.Close()
			a, err := New(WithProvider(ProviderConfig{Protocol: tt.protocol, Type: consts.ProviderTypeOpenCodeGo, BaseURL: server.URL + tt.base, RequestPath: tt.path, APIKey: "test", ModelID: "test", ConversationID: "123"}))
			if err != nil {
				t.Fatal(err)
			}
			retries := 0
			_, streamErr := a.Stream(context.Background(), fantasy.AgentStreamCall{Prompt: "hello", MaxRetries: &retries})
			if streamErr == nil {
				t.Fatal("expected HTTP error")
			}
			// 路径以协议端点结尾时，错误信息里的地址也必须是真实请求地址。
			wantURL := server.URL + tt.want
			var providerErr *fantasy.ProviderError
			if !errors.As(streamErr, &providerErr) {
				t.Fatalf("stream error %v is not a ProviderError", streamErr)
			}
			if strings.HasSuffix(tt.want, "/"+protocolSDKPath[tt.protocol]) {
				if providerErr.URL != wantURL {
					t.Errorf("stream error %v does not expose real URL %q", streamErr, wantURL)
				}
			} else if providerErr.URL == wantURL {
				t.Errorf("fallback rewrite should keep SDK request URL in the error, got %q", providerErr.URL)
			}
			if _, err := a.Generate(context.Background(), fantasy.AgentCall{Prompt: "title", MaxRetries: &retries}); err == nil {
				t.Fatal("expected HTTP error")
			}
			if calls != 2 {
				t.Fatalf("calls = %d", calls)
			}
		})
	}
}

// 所有提供商共享独立的 Transport：不能改动 http.DefaultTransport，且必须有响应头超时。
func TestProviderTransportConfiguration(t *testing.T) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("default transport is not *http.Transport")
	}
	transport, ok := newProviderTransport().(*http.Transport)
	if !ok {
		t.Fatal("provider transport is not *http.Transport")
	}
	if transport == base {
		t.Fatal("provider transport must not mutate http.DefaultTransport")
	}
	if transport.ResponseHeaderTimeout != providerHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout=%s", transport.ResponseHeaderTimeout)
	}
	if transport.MaxIdleConnsPerHost != 16 {
		t.Fatalf("MaxIdleConnsPerHost=%d", transport.MaxIdleConnsPerHost)
	}
}

// BaseURL 与 request_path 缺一不可；拼接结果必须是完整的 HTTP(S) URL。
func TestProviderRequestURLRequired(t *testing.T) {
	tests := []struct {
		name string
		base string
		path string
	}{
		{name: "empty-base", base: "", path: "/chat/completions"},
		{name: "empty-path", base: "https://example.com/v1", path: ""},
		{name: "missing-scheme", base: "example.com", path: "/v1/responses"},
		{name: "ftp-scheme", base: "ftp://example.com", path: "/v1/responses"},
		{name: "empty-host", base: "https://", path: "/v1/responses"},
		{name: "invalid-escape", base: "https://%", path: "/v1/responses"},
		{name: "leading-space", base: " https://example.com/v1/responses", path: "/v1/responses"},
		{name: "userinfo", base: "https://user:secret@example.com/api", path: "/v1/responses"},
		{name: "fragment", base: "https://example.com/api#fragment", path: "/v1/responses"},
		{name: "query", base: "https://example.com/v1?key=a", path: "/v1/responses"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(WithProvider(ProviderConfig{Protocol: consts.ProtocolOpenAIChat, BaseURL: tt.base, RequestPath: tt.path, APIKey: "test", ModelID: "test"})); err == nil {
				t.Errorf("expected invalid request URL error for %q + %q", tt.base, tt.path)
			}
		})
	}
}

// 协议缺失或未知时必须在组装阶段报错，不能退回默认端点。
func TestProviderProtocolRequired(t *testing.T) {
	for _, protocol := range []consts.ProviderProtocol{"", "unknown"} {
		if _, err := New(WithProvider(ProviderConfig{Protocol: protocol, BaseURL: "https://example.com/v1", RequestPath: "/chat/completions", APIKey: "test", ModelID: "test"})); err == nil {
			t.Errorf("expected unsupported protocol error for %q", protocol)
		}
	}
}
