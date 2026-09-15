package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/openai"

	"github.com/lyonmu/kaguya/internal/consts"
)

// OpenAI Chat：开启思考按强度映射，关闭思考显式传 none。
func TestReasoningProviderOptionsOpenAIChat(t *testing.T) {
	tests := []struct {
		name    string
		enabled consts.Status
		effort  consts.ReasoningEffort
		want    openai.ReasoningEffort
	}{
		{name: "enabled-minimal", enabled: consts.IsTrue, effort: consts.ReasoningEffortMinimal, want: openai.ReasoningEffortMinimal},
		{name: "enabled-low", enabled: consts.IsTrue, effort: consts.ReasoningEffortLow, want: openai.ReasoningEffortLow},
		{name: "enabled-medium", enabled: consts.IsTrue, effort: consts.ReasoningEffortMedium, want: openai.ReasoningEffortMedium},
		{name: "enabled-high", enabled: consts.IsTrue, effort: consts.ReasoningEffortHigh, want: openai.ReasoningEffortHigh},
		{name: "enabled-xhigh", enabled: consts.IsTrue, effort: consts.ReasoningEffortXHigh, want: openai.ReasoningEffortXHigh},
		{name: "enabled-max", enabled: consts.IsTrue, effort: consts.ReasoningEffortMax, want: openai.ReasoningEffortMax},
		{name: "disabled", enabled: consts.IsFalse, effort: consts.ReasoningEffortHigh, want: openai.ReasoningEffortNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options, err := reasoningProviderOptions(ProviderConfig{
				Protocol: consts.ProtocolOpenAIChat, ReasoningEnabled: tt.enabled, ReasoningEffort: tt.effort,
			})
			if err != nil {
				t.Fatal(err)
			}
			got, ok := options[openai.Name].(*openai.ProviderOptions)
			if !ok {
				t.Fatalf("provider options = %#v", options)
			}
			if got.ReasoningEffort == nil || *got.ReasoningEffort != tt.want {
				t.Fatalf("reasoning effort = %v, want %q", got.ReasoningEffort, tt.want)
			}
		})
	}
}

// OpenAI Responses：与 Chat 使用不同的 ProviderOptions 类型，强度映射规则一致。
func TestReasoningProviderOptionsOpenAIResponses(t *testing.T) {
	tests := []struct {
		name    string
		enabled consts.Status
		effort  consts.ReasoningEffort
		want    openai.ReasoningEffort
	}{
		{name: "enabled-minimal", enabled: consts.IsTrue, effort: consts.ReasoningEffortMinimal, want: openai.ReasoningEffortMinimal},
		{name: "enabled-low", enabled: consts.IsTrue, effort: consts.ReasoningEffortLow, want: openai.ReasoningEffortLow},
		{name: "enabled-medium", enabled: consts.IsTrue, effort: consts.ReasoningEffortMedium, want: openai.ReasoningEffortMedium},
		{name: "enabled-high", enabled: consts.IsTrue, effort: consts.ReasoningEffortHigh, want: openai.ReasoningEffortHigh},
		{name: "enabled-xhigh", enabled: consts.IsTrue, effort: consts.ReasoningEffortXHigh, want: openai.ReasoningEffortXHigh},
		{name: "enabled-max", enabled: consts.IsTrue, effort: consts.ReasoningEffortMax, want: openai.ReasoningEffortMax},
		{name: "disabled", enabled: consts.IsFalse, effort: consts.ReasoningEffortHigh, want: openai.ReasoningEffortNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options, err := reasoningProviderOptions(ProviderConfig{
				Protocol: consts.ProtocolOpenAIResponses, ReasoningEnabled: tt.enabled, ReasoningEffort: tt.effort,
			})
			if err != nil {
				t.Fatal(err)
			}
			got, ok := options[openai.Name].(*openai.ResponsesProviderOptions)
			if !ok {
				t.Fatalf("provider options = %#v", options)
			}
			if got.ReasoningEffort == nil || *got.ReasoningEffort != tt.want {
				t.Fatalf("reasoning effort = %v, want %q", got.ReasoningEffort, tt.want)
			}
		})
	}
}

// Anthropic：开启思考映射 Effort，关闭思考不设置 Effort，也不使用 Thinking/显示字段作为开关。
func TestReasoningProviderOptionsAnthropic(t *testing.T) {
	tests := []struct {
		effort consts.ReasoningEffort
		want   anthropic.Effort
	}{
		{effort: consts.ReasoningEffortLow, want: anthropic.EffortLow},
		{effort: consts.ReasoningEffortMedium, want: anthropic.EffortMedium},
		{effort: consts.ReasoningEffortHigh, want: anthropic.EffortHigh},
		{effort: consts.ReasoningEffortXHigh, want: anthropic.EffortXHigh},
		{effort: consts.ReasoningEffortMax, want: anthropic.EffortMax},
	}
	for _, tt := range tests {
		t.Run(string(tt.effort), func(t *testing.T) {
			options, err := reasoningProviderOptions(ProviderConfig{
				Protocol: consts.ProtocolAnthropic, ReasoningEnabled: consts.IsTrue, ReasoningEffort: tt.effort,
			})
			if err != nil {
				t.Fatal(err)
			}
			got, ok := options[anthropic.Name].(*anthropic.ProviderOptions)
			if !ok {
				t.Fatalf("provider options = %#v", options)
			}
			if got.Effort == nil || *got.Effort != tt.want {
				t.Fatalf("effort = %v, want %q", got.Effort, tt.want)
			}
			if got.Thinking != nil || got.SendReasoning != nil || got.ThinkingDisplay != nil {
				t.Fatalf("reasoning switch must not use thinking display options: %#v", got)
			}
		})
	}
	t.Run("disabled", func(t *testing.T) {
		options, err := reasoningProviderOptions(ProviderConfig{
			Protocol: consts.ProtocolAnthropic, ReasoningEnabled: consts.IsFalse, ReasoningEffort: consts.ReasoningEffortHigh,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(options) != 0 {
			t.Fatalf("provider options = %#v, want none", options)
		}
	})
}

// 启用思考时非法强度必须直接报错，不能回退为其他强度。
func TestReasoningProviderOptionsRejectsUnknownEffort(t *testing.T) {
	protocols := []consts.ProviderProtocol{
		consts.ProtocolOpenAIChat, consts.ProtocolOpenAIResponses, consts.ProtocolAnthropic,
	}
	for _, protocol := range protocols {
		t.Run(string(protocol), func(t *testing.T) {
			_, err := reasoningProviderOptions(ProviderConfig{
				Protocol: protocol, ReasoningEnabled: consts.IsTrue, ReasoningEffort: "ultra",
			})
			if err == nil || !strings.Contains(err.Error(), `"ultra"`) {
				t.Fatalf("error = %v, want unsupported reasoning effort", err)
			}
		})
	}
}

// Anthropic 没有 minimal 等级，必须明确报错而不是降级为 low。
func TestAnthropicRejectsMinimalEffort(t *testing.T) {
	_, err := reasoningProviderOptions(ProviderConfig{
		Protocol: consts.ProtocolAnthropic, ReasoningEnabled: consts.IsTrue, ReasoningEffort: consts.ReasoningEffortMinimal,
	})
	if err == nil || !strings.Contains(err.Error(), `"minimal"`) {
		t.Fatalf("error = %v, want unsupported reasoning effort", err)
	}
}

// 组装 Agent 时就要暴露非法强度，不能等到调用模型才失败。
func TestNewRejectsUnknownReasoningEffort(t *testing.T) {
	_, err := New(WithProvider(ProviderConfig{
		Protocol: consts.ProtocolOpenAIChat, BaseURL: "https://example.com/v1", RequestPath: "/chat/completions",
		APIKey: "test", ModelID: "test",
		ReasoningEnabled: consts.IsTrue, ReasoningEffort: "ultra",
	}))
	if err == nil || !strings.Contains(err.Error(), `"ultra"`) {
		t.Fatalf("error = %v, want unsupported reasoning effort", err)
	}
}

// 思考选项必须真正进入请求：OpenAI 两种协议映射到对应字段，Anthropic 关闭时不发送思考参数。
func TestReasoningProviderOptionsReachRequest(t *testing.T) {
	const (
		chatResponse      = `{"id":"chat_test","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
		responsesResponse = `{"id":"resp_test","object":"response","status":"completed","model":"test","output":[{"id":"msg_test","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
		anthropicResponse = `{"id":"msg_test","type":"message","role":"assistant","model":"test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	)
	tests := []struct {
		name     string
		protocol consts.ProviderProtocol
		modelID  string
		enabled  consts.Status
		effort   consts.ReasoningEffort
		response string
		check    func(t *testing.T, body map[string]any)
	}{
		{
			name: "openai-chat-high", protocol: consts.ProtocolOpenAIChat, modelID: "test",
			enabled: consts.IsTrue, effort: consts.ReasoningEffortHigh, response: chatResponse,
			check: func(t *testing.T, body map[string]any) {
				if body["reasoning_effort"] != string(openai.ReasoningEffortHigh) {
					t.Errorf("reasoning_effort = %v", body["reasoning_effort"])
				}
			},
		},
		{
			name: "openai-responses-low", protocol: consts.ProtocolOpenAIResponses, modelID: "gpt-5.2",
			enabled: consts.IsTrue, effort: consts.ReasoningEffortLow, response: responsesResponse,
			check: func(t *testing.T, body map[string]any) {
				reasoning, ok := body["reasoning"].(map[string]any)
				if !ok || reasoning["effort"] != string(openai.ReasoningEffortLow) {
					t.Errorf("reasoning = %#v", body["reasoning"])
				}
			},
		},
		{
			name: "anthropic-disabled", protocol: consts.ProtocolAnthropic, modelID: "test",
			enabled: consts.IsFalse, effort: consts.ReasoningEffortHigh, response: anthropicResponse,
			check: func(t *testing.T, body map[string]any) {
				if _, ok := body["effort"]; ok {
					t.Errorf("unexpected effort = %v", body["effort"])
				}
				if _, ok := body["thinking"]; ok {
					t.Errorf("unexpected thinking = %v", body["thinking"])
				}
			},
		},
	}
	requestPaths := map[consts.ProviderProtocol]string{
		consts.ProtocolOpenAIChat:      "/chat/completions",
		consts.ProtocolOpenAIResponses: "/responses",
		consts.ProtocolAnthropic:       "/messages",
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request body: %v", err)
				} else {
					tt.check(t, body)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tt.response)
			}))
			defer server.Close()

			a, err := New(WithProvider(ProviderConfig{
				Protocol: tt.protocol, BaseURL: server.URL, RequestPath: requestPaths[tt.protocol],
				APIKey: "test", ModelID: tt.modelID,
				ReasoningEnabled: tt.enabled, ReasoningEffort: tt.effort,
			}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Generate(context.Background(), fantasy.AgentCall{Prompt: "hello"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
