package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/consts"
)

func TestProviderTypeHeaders(t *testing.T) {
	for _, protocol := range []consts.ProviderProtocol{consts.ProtocolOpenAIChat, consts.ProtocolOpenAIResponses, consts.ProtocolAnthropic} {
		for _, kind := range []consts.ProviderType{"", consts.ProviderTypeNormal, consts.ProviderTypeOpenCodeGo} {
			for _, session := range []string{"", "123456789012345"} {
				t.Run(string(protocol)+"/"+string(kind)+"/"+session, func(t *testing.T) {
					calls := 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						want := ""
						if kind == consts.ProviderTypeOpenCodeGo {
							want = session
						}
						if got := r.Header.Get("x-opencode-session"); got != want {
							t.Errorf("opencode session = %q, want %q", got, want)
						}
						if r.Header.Get("X-Conversation-ID") != session {
							t.Error("existing session header changed")
						}
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"test"}}`))
					}))
					defer server.Close()
					a, err := New(WithProvider(ProviderConfig{Type: kind, Protocol: protocol, BaseURL: server.URL, APIKey: "test", ModelID: "test", ConversationID: session}))
					if err != nil {
						t.Fatal(err)
					}
					retries := 0
					if _, err := a.Stream(context.Background(), fantasy.AgentStreamCall{Prompt: "hello", MaxRetries: &retries}); err == nil {
						t.Fatal("expected HTTP failure")
					}
					if _, err := a.Generate(context.Background(), fantasy.AgentCall{Prompt: "title", MaxRetries: &retries}); err == nil {
						t.Fatal("expected HTTP failure")
					}
					if calls != 2 {
						t.Fatalf("calls = %d", calls)
					}
				})
			}
		}
	}
}
