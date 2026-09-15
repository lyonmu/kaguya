package system

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
)

const modelTestReply = `{"id":"chat_test","object":"chat.completion","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hi there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

// 模型测试走真实的运行时与 HTTP 请求，因此用本地服务替代提供商，只验证发出的请求与结论。
func TestModelTest(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}

	t.Run("succeeds with the provider reply", func(t *testing.T) {
		var auth, path, body string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth, path = r.Header.Get("Authorization"), r.URL.Path
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(modelTestReply))
		}))
		defer server.Close()

		provider, err := svc.ProviderCreate(ctx, &dtosystem.SystemProviderSaveReq{
			ProviderName: "test-success", APIKey: "sk-test", BaseURL: server.URL,
		})
		if err != nil {
			t.Fatalf("create provider: %v", err)
		}
		resp, err := svc.ModelTest(ctx, modelSaveReq(provider.ID, "Test", "test-model"))
		if err != nil {
			t.Fatalf("model test: %v", err)
		}
		if resp.Reply != "Hi there" {
			t.Fatalf("reply = %q", resp.Reply)
		}
		// 请求使用提供商 BaseURL 与请求中路径的拼接地址，并带上解密后的密钥。
		if path != "/v1/chat/completions" {
			t.Errorf("request path = %q", path)
		}
		if auth != "Bearer sk-test" {
			t.Errorf("authorization = %q", auth)
		}
		if !strings.Contains(body, `"content":"Hi!"`) {
			t.Errorf("prompt was not sent: %s", body)
		}
	})

	t.Run("reports the provider error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"invalid api key"}}`))
		}))
		defer server.Close()

		provider, err := svc.ProviderCreate(ctx, &dtosystem.SystemProviderSaveReq{
			ProviderName: "test-unauthorized", APIKey: "sk-test", BaseURL: server.URL,
		})
		if err != nil {
			t.Fatalf("create provider: %v", err)
		}
		_, err = svc.ModelTest(ctx, modelSaveReq(provider.ID, "Test", "test-model"))
		if !errors.Is(err, ErrModelTest) {
			t.Fatalf("expected ErrModelTest, got %v", err)
		}
		if !strings.Contains(err.Error(), "invalid api key") {
			t.Fatalf("provider message must be kept: %v", err)
		}
	})

	t.Run("rejects unknown provider and malformed path", func(t *testing.T) {
		missing := modelSaveReq("missing", "Test", "test-model")
		if _, err := svc.ModelTest(ctx, missing); !errors.Is(err, ErrProviderNotFound) {
			t.Fatalf("expected ErrProviderNotFound, got %v", err)
		}

		provider, err := svc.ProviderCreate(ctx, &dtosystem.SystemProviderSaveReq{
			ProviderName: "test-path", APIKey: "sk-test", BaseURL: "https://api.example.com",
		})
		if err != nil {
			t.Fatalf("create provider: %v", err)
		}
		invalid := modelSaveReq(provider.ID, "Test", "test-model")
		invalid.RequestPath = "v1/chat/completions"
		if _, err := svc.ModelTest(ctx, invalid); !errors.Is(err, ErrModelRequestPath) {
			t.Fatalf("expected ErrModelRequestPath, got %v", err)
		}
	})
}
