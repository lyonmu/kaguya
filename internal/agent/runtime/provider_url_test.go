package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/consts"
)

// 使用真实 SDK 验证最终 HTTP 请求，不能仅测试传给 SDK 的 BaseURL。
func TestProviderRequestURL(t *testing.T) {
	for _, protocol := range []consts.ProviderProtocol{consts.ProtocolOpenAIChat, consts.ProtocolOpenAIResponses, consts.ProtocolAnthropic} {
		for _, path := range []string{
			"", "/", "/v1", "/v1/", "/v1/responses", "/v1/chat/completions", "/v1/messages",
			"/go/v1/chat/completions", "/custom/endpoint/", "/gateway//endpoint",
			"/custom%2Fendpoint?version=1&key=a%2Fb&key=c",
		} {
			t.Run(string(protocol)+path, func(t *testing.T) {
				want := path
				if want == "" {
					want = "/"
				} // HTTP 的空路径仅表示根路径，不自动补端点。
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.RequestURI != want {
						t.Errorf("URI = %q, want %q", r.RequestURI, want)
					}
					if r.Header.Get("User-Agent") != "ConfiguredAgent/1.0" {
						t.Errorf("User-Agent=%q", r.Header.Get("User-Agent"))
					}
					if r.Method != http.MethodPost {
						t.Errorf("method = %s", r.Method)
					}
					if r.Header.Get("X-Conversation-ID") != "123" || r.Header.Get("x-opencode-session") != "123" {
						t.Error("missing session headers")
					}
					if protocol == consts.ProtocolAnthropic {
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
				a, err := New(WithProvider(ProviderConfig{Protocol: protocol, Type: consts.ProviderTypeOpenCodeGo, BaseURL: server.URL + path, APIKey: "test", ModelID: "test", ConversationID: "123", UserAgent: "ConfiguredAgent/1.0"}))
				if err != nil {
					t.Fatal(err)
				}
				retries := 0
				if _, err := a.Stream(context.Background(), fantasy.AgentStreamCall{Prompt: "hello", MaxRetries: &retries}); err == nil {
					t.Fatal("expected HTTP error")
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

func TestProviderRequestURLRequired(t *testing.T) {
	for _, input := range []string{"", "example.com", "/v1/responses", "ftp://example.com", "https://", "https://%", " https://example.com/v1/responses", "https://user:secret@example.com/api", "https://example.com/api#fragment"} {
		if _, err := New(WithProvider(ProviderConfig{Protocol: consts.ProtocolOpenAIChat, BaseURL: input, APIKey: "test", ModelID: "test"})); err == nil {
			t.Errorf("expected invalid URL error for %q", input)
		}
	}
}
