package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"

	token "github.com/lyonmu/kaguya/internal/agent/token"
	"github.com/lyonmu/kaguya/internal/consts"
)

// 真实模型对话流程测试：验证 WithProvider 组装 + 多轮对话的完整链路。
// 使用方式：在下方常量中填入真实配置，然后运行
//
//	go test -v -run TestProviderConversationFlow ./internal/agent/runtime/
//
// 未填 API Key 时测试自动跳过，不影响普通单元测试。
const (
	flowProviderName = ""                        // 提供商名称（仅用于运行信息记录）
	flowProtocol     = consts.ProtocolOpenAIChat // 协议：openai-chat / openai-response / anthropic
	flowBaseURL      = ""                        // 必填完整请求 URL，例如 https://api.openai.com/v1/chat/completions
	flowAPIKey       = ""                        // API Key
	flowModelID      = ""                        // 模型 ID，例如 gpt-4o-mini
)

// flowRecorder 把每次回答的运行信息打印到测试日志，验证记录链路。
type flowRecorder struct {
	t *testing.T
}

func (r *flowRecorder) RecordUsage(_ context.Context, usage token.TurnUsage) error {
	r.t.Logf("运行信息: mode=%s 总耗时=%v 思考时长=%v 工具调用=%d 结束原因=%s 输入token=%d 输出token=%d 总token=%d 缓存命中=%d 思考token=%d",
		usage.Mode, usage.TotalDuration, usage.ReasoningDuration, usage.ToolCalls,
		usage.FinishReason, usage.Total.InputTokens, usage.Total.OutputTokens,
		usage.Total.TotalTokens, usage.Total.CacheHitTokens, usage.Total.ReasoningTokens)
	return nil
}

// TestProviderConversationFlow 用真实提供商配置跑一段完整的对话流程：
//   - 第一轮：Generate 发起提问；
//   - 第二轮：携带第一轮的完整历史继续追问，验证上下文传递；
//   - 第三轮：Stream 流式回答，验证流式链路与思考时长统计。
func TestProviderConversationFlow(t *testing.T) {
	if flowAPIKey == "" {
		t.Skip("未配置 API Key，请在 conversation_test.go 顶部常量中填入后运行")
	}

	a, err := New(
		WithProvider(ProviderConfig{
			Name:     flowProviderName,
			Protocol: flowProtocol,
			BaseURL:  flowBaseURL,
			APIKey:   flowAPIKey,
			ModelID:  flowModelID,
		}),
		WithSystemPrompt("你是一个乐于助人的 AI 助手。"),
		WithRecorder(&flowRecorder{t: t}),
	)
	if err != nil {
		t.Fatalf("组装 Agent 失败: %v", err)
	}

	ctx := token.WithConversationID(
		token.WithMessageID(context.Background(), "flow-msg-1"),
		"flow-conv-1",
	)

	// 多轮历史：每轮结束后把该轮的 assistant/tool 消息追加进来
	var history []fantasy.Message
	appendHistory := func(result *fantasy.AgentResult) {
		for _, step := range result.Steps {
			history = append(history, step.Messages...)
		}
	}

	// ---- 第一轮：Generate ----
	t.Log("===== 第一轮（Generate）=====")
	first, err := a.Generate(ctx, fantasy.AgentCall{Prompt: "你好，请介绍一下你自己。"})
	if err != nil {
		t.Fatalf("第一轮 Generate: %v", err)
	}
	t.Logf("回答: %s", first.Response.Content.Text())
	appendHistory(first)

	// ---- 第二轮：携带历史继续追问 ----
	t.Log("===== 第二轮（携带历史 Generate）=====")
	second, err := a.Generate(ctx, fantasy.AgentCall{
		Prompt:   "我刚才问了什么？请复述我的问题。",
		Messages: history,
	})
	if err != nil {
		t.Fatalf("第二轮 Generate: %v", err)
	}
	t.Logf("回答: %s", second.Response.Content.Text())
	appendHistory(second)

	// ---- 第三轮：Stream 流式回答 ----
	t.Log("===== 第三轮（Stream）=====")
	third, err := a.Stream(ctx, fantasy.AgentStreamCall{
		Prompt:   "好的，请用一句话总结我们刚才的对话。",
		Messages: history,
		OnTextDelta: func(_ string, delta string) error {
			t.Logf("流式片段: %s", delta)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("第三轮 Stream: %v", err)
	}
	t.Logf("回答: %s", third.Response.Content.Text())

	// 三轮回答都不能为空
	for i, result := range []*fantasy.AgentResult{first, second, third} {
		if result.Response.Content.Text() == "" {
			t.Fatalf("第 %d 轮回答为空", i+1)
		}
	}
}
