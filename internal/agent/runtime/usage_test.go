package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"charm.land/fantasy"
	token "github.com/lyonmu/kaguya/internal/agent/token"
	"github.com/lyonmu/kaguya/internal/consts"
)

// 使用真实 provider 适配器解析 HTTP 响应，避免只验证手工构造的 fantasy.Usage。
func TestProviderUsage(t *testing.T) {
	tests := []struct {
		name     string
		protocol consts.ProviderProtocol
		body     string
		stream   bool
		want     token.NormalizedUsage
	}{
		{
			name: "responses", protocol: consts.ProtocolOpenAIResponses,
			body: `{"id":"resp_test","object":"response","status":"completed","model":"test","output":[{"id":"msg_test","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":117,"input_tokens_details":{"cache_write_tokens":0,"cached_tokens":117},"output_tokens":340,"output_tokens_details":{"reasoning_tokens":284},"total_tokens":457}}`,
			want: token.NormalizedUsage{OutputTokens: 340, TotalTokens: 457, CacheHitTokens: 117, ReasoningTokens: 284},
		},
		{
			name: "chat", protocol: consts.ProtocolOpenAIChat,
			body: `{"id":"chat_test","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":91,"completion_tokens":32,"total_tokens":123,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":22},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":91}}`,
			want: token.NormalizedUsage{InputTokens: 91, OutputTokens: 32, TotalTokens: 123, ReasoningTokens: 22},
		},
		{
			name: "anthropic", protocol: consts.ProtocolAnthropic,
			body: `{"id":"msg_test","type":"message","role":"assistant","model":"test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":24,"output_tokens":146,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"prompt_tokens_details":{"cached_tokens":0}}}`,
			want: token.NormalizedUsage{InputTokens: 24, OutputTokens: 146, TotalTokens: 170},
		},
		{
			name: "chat stream with cache and reasoning", protocol: consts.ProtocolOpenAIChat, stream: true,
			body: "data: {\"id\":\"chat_test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat_test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":117,\"completion_tokens\":340,\"total_tokens\":457,\"prompt_tokens_details\":{\"cached_tokens\":117},\"completion_tokens_details\":{\"reasoning_tokens\":284}}}\n\ndata: [DONE]\n\n",
			want: token.NormalizedUsage{OutputTokens: 340, TotalTokens: 457, CacheHitTokens: 117, ReasoningTokens: 284},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("X-Conversation-ID"); got != "123456789012345" {
					t.Errorf("conversation header = %q", got)
				}
				wantPath := "/chat/completions"
				switch tt.protocol {
				case consts.ProtocolOpenAIResponses:
					wantPath = "/responses"
				case consts.ProtocolAnthropic:
					wantPath = "/v1/messages"
				}
				if r.URL.Path != wantPath {
					t.Errorf("request path = %s, want %s", r.URL.Path, wantPath)
				}
				if tt.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			recorder := &usageTestRecorder{}
			a, err := New(WithProvider(ProviderConfig{Protocol: tt.protocol, BaseURL: server.URL, APIKey: "test", ModelID: "test", ConversationID: "123456789012345"}), WithRecorder(recorder))
			if err != nil {
				t.Fatal(err)
			}
			var result *fantasy.AgentResult
			if tt.stream {
				result, err = a.Stream(context.Background(), fantasy.AgentStreamCall{Prompt: "hello"})
			} else {
				result, err = a.Generate(context.Background(), fantasy.AgentCall{Prompt: "hello"})
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := token.FromFantasyUsage(result.TotalUsage); got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			if recorder.usage.Total != tt.want {
				t.Fatalf("recorded %+v, want %+v", recorder.usage.Total, tt.want)
			}
			if len(recorder.usage.Steps) != 1 || recorder.usage.Steps[0].Usage != tt.want {
				t.Fatalf("unexpected steps: %+v", recorder.usage.Steps)
			}
		})
	}
}

type usageTestRecorder struct{ usage token.TurnUsage }

func (r *usageTestRecorder) RecordUsage(_ context.Context, usage token.TurnUsage) error {
	r.usage = usage
	return nil
}
