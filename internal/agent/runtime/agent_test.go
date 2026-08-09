package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"

	token "github.com/lyonmu/kaguya/internal/agent/token"
)

// TestNew_Validation 校验组装约束：必须有且只能有一个模型来源，协议必须受支持。
func TestNew_Validation(t *testing.T) {
	tests := []struct {
		name    string
		opts    []Option
		wantErr string // 期望错误信息包含的片段
	}{
		{
			name:    "no model source",
			opts:    nil,
			wantErr: "model is required",
		},
		{
			name:    "nil model ignored",
			opts:    []Option{WithModel(nil)},
			wantErr: "model is required",
		},
		{
			name:    "both model sources",
			opts:    []Option{WithModel(&fakeModel{}), WithProvider(ProviderConfig{Protocol: ProtocolOpenAI})},
			wantErr: "configured more than once",
		},
		{
			name: "unsupported protocol",
			opts: []Option{WithProvider(ProviderConfig{
				Protocol: "unknown-proto",
				APIKey:   "k",
				ModelID:  "m",
			})},
			wantErr: "unsupported provider protocol",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := New(tt.opts...)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil agent=%v", tt.wantErr, a)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

// TestNew_WithProvider_Protocols 验证所有受支持协议均可成功组装（仅构造本地对象，不发起网络请求）。
func TestNew_WithProvider_Protocols(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
	}{
		{name: "openai", protocol: ProtocolOpenAI},
		{name: "anthropic", protocol: ProtocolAnthropic},
		{name: "openaicompat", protocol: ProtocolOpenAICompat},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := New(WithProvider(ProviderConfig{
				Name:     "my-" + tt.name,
				Protocol: tt.protocol,
				BaseURL:  "https://example.com/v1",
				APIKey:   "sk-test",
				ModelID:  "gpt-4o",
			}))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if a == nil {
				t.Fatal("expected non-nil agent")
			}
			if a.provider != "my-"+tt.name {
				t.Fatalf("provider = %q, want %q", a.provider, "my-"+tt.name)
			}
			if a.model != "gpt-4o" {
				t.Fatalf("model = %q, want %q", a.model, "gpt-4o")
			}
		})
	}
}

// TestNew_WithModel_SetsMetadata 验证 WithModel 自动派生记录元数据。
func TestNew_WithModel_SetsMetadata(t *testing.T) {
	m := &fakeModel{provider: "test-provider", model: "test-model"}
	a, err := New(WithModel(m))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.provider != "test-provider" {
		t.Fatalf("provider = %q, want %q", a.provider, "test-provider")
	}
	if a.model != "test-model" {
		t.Fatalf("model = %q, want %q", a.model, "test-model")
	}
}

// fakeModel 脚本化返回 Generate 响应与 Stream 流，Provider()/Model() 返回固定元数据。
type fakeModel struct {
	provider string
	model    string

	responses     []*fantasy.Response      // Generate 按调用顺序依次返回
	streams       []fantasy.StreamResponse // Stream 按调用顺序依次返回
	generateErr   error                    // 非空时 Generate 直接返回该错误（错误场景测试用）
	generateCalls int
	streamCalls   int
}

func (m *fakeModel) Generate(_ context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	if m.generateErr != nil {
		return nil, m.generateErr
	}
	r := m.responses[m.generateCalls]
	m.generateCalls++
	return r, nil
}

func (m *fakeModel) Stream(_ context.Context, _ fantasy.Call) (fantasy.StreamResponse, error) {
	s := m.streams[m.streamCalls]
	m.streamCalls++
	return s, nil
}

func (m *fakeModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, nil
}

func (m *fakeModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, nil
}

func (m *fakeModel) Provider() string { return m.provider }
func (m *fakeModel) Model() string    { return m.model }

// fakeRecorder 内存记录器，可注入错误以验证记录失败不影响回答。
type fakeRecorder struct {
	mu    sync.Mutex
	items []token.TurnUsage
	err   error
}

func (r *fakeRecorder) RecordUsage(_ context.Context, usage token.TurnUsage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.items = append(r.items, usage)
	return nil
}

func (r *fakeRecorder) Items() []token.TurnUsage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]token.TurnUsage, len(r.items))
	copy(out, r.items)
	return out
}

// stopResponse 构造一个带文本内容、Stop 结束的响应。
func stopResponse(input, output int64) *fantasy.Response {
	return &fantasy.Response{
		Content:      fantasy.ResponseContent{fantasy.TextContent{Text: "done"}},
		FinishReason: fantasy.FinishReasonStop,
		Usage:        fantasy.Usage{InputTokens: input, OutputTokens: output},
	}
}

// stopStream 构造一个带思考片段 + 文本片段 + Finish 的流。
func stopStream(usage fantasy.Usage) fantasy.StreamResponse {
	return func(yield func(fantasy.StreamPart) bool) {
		parts := []fantasy.StreamPart{
			{Type: fantasy.StreamPartTypeReasoningStart, ID: "r1", Delta: "思考中"},
			{Type: fantasy.StreamPartTypeReasoningDelta, ID: "r1", Delta: "……"},
			{Type: fantasy.StreamPartTypeReasoningEnd, ID: "r1"},
			{Type: fantasy.StreamPartTypeTextStart, ID: "t1"},
			{Type: fantasy.StreamPartTypeTextDelta, ID: "t1", Delta: "你好"},
			{Type: fantasy.StreamPartTypeTextEnd, ID: "t1"},
			{Type: fantasy.StreamPartTypeFinish, Usage: usage, FinishReason: fantasy.FinishReasonStop},
		}
		for _, p := range parts {
			if !yield(p) {
				return
			}
		}
	}
}

// TestGenerate_RecordsUsage 验证 Generate 记录 provider/model/mode/时长/token/结束原因。
func TestGenerate_RecordsUsage(t *testing.T) {
	model := &fakeModel{
		provider:  "test-provider",
		model:     "test-model",
		responses: []*fantasy.Response{stopResponse(10, 20)},
	}
	recorder := &fakeRecorder{}

	a, err := New(WithModel(model), WithRecorder(recorder))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := a.Generate(context.Background(), fantasy.AgentCall{Prompt: "hello"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	items := recorder.Items()
	if len(items) != 1 {
		t.Fatalf("expected 1 usage item, got %d", len(items))
	}
	u := items[0]
	if u.Provider != "test-provider" || u.Model != "test-model" {
		t.Fatalf("provider/model = %q/%q, want test-provider/test-model", u.Provider, u.Model)
	}
	if u.Mode != "generate" {
		t.Fatalf("mode = %q, want generate", u.Mode)
	}
	if u.TotalDuration <= 0 {
		t.Fatalf("TotalDuration = %v, want > 0", u.TotalDuration)
	}
	if u.ReasoningDuration != 0 {
		t.Fatalf("ReasoningDuration = %v, want 0 (generate 模式不可测)", u.ReasoningDuration)
	}
	if u.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q, want stop", u.FinishReason)
	}
	if u.ToolCalls != 0 {
		t.Fatalf("ToolCalls = %d, want 0", u.ToolCalls)
	}
	if u.Total.InputTokens != 10 || u.Total.OutputTokens != 20 || u.Total.TotalTokens != 30 {
		t.Fatalf("total usage = %+v, want input=10 output=20 total=30", u.Total)
	}
	if len(u.Steps) != 1 || u.Steps[0].StepIndex != 1 || u.Steps[0].ToolCalls != 0 {
		t.Fatalf("steps = %+v, want 1 step index=1 tool_calls=0", u.Steps)
	}
	if u.Err != "" {
		t.Fatalf("Err = %q, want empty", u.Err)
	}
}

// TestGenerate_RecordsContextIDs 验证 context 注入的会话/消息 ID 落入记录（默认 ID 取值函数从 context 读取）。
func TestGenerate_RecordsContextIDs(t *testing.T) {
	model := &fakeModel{
		provider:  "test-provider",
		model:     "test-model",
		responses: []*fantasy.Response{stopResponse(1, 1)},
	}
	recorder := &fakeRecorder{}

	a, err := New(WithModel(model), WithRecorder(recorder))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := token.WithConversationID(context.Background(), "conv-42")
	ctx = token.WithMessageID(ctx, "msg-42")
	if _, err := a.Generate(ctx, fantasy.AgentCall{Prompt: "hello"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	u := recorder.Items()[0]
	if u.ConversationID != "conv-42" {
		t.Fatalf("ConversationID = %q, want %q", u.ConversationID, "conv-42")
	}
	if u.MessageID != "msg-42" {
		t.Fatalf("MessageID = %q, want %q", u.MessageID, "msg-42")
	}
}

// TestGenerate_ContextIDFuncOverrides 验证 WithConversationIDFunc/WithMessageIDFunc 覆盖默认的 context 读取。
func TestGenerate_ContextIDFuncOverrides(t *testing.T) {
	model := &fakeModel{
		provider:  "test-provider",
		model:     "test-model",
		responses: []*fantasy.Response{stopResponse(1, 1)},
	}
	recorder := &fakeRecorder{}

	a, err := New(
		WithModel(model),
		WithRecorder(recorder),
		WithConversationIDFunc(func(context.Context) string { return "override-conv" }),
		WithMessageIDFunc(func(context.Context) string { return "override-msg" }),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := token.WithConversationID(context.Background(), "ctx-conv")
	ctx = token.WithMessageID(ctx, "ctx-msg")
	if _, err := a.Generate(ctx, fantasy.AgentCall{Prompt: "hello"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	u := recorder.Items()[0]
	if u.ConversationID != "override-conv" {
		t.Fatalf("ConversationID = %q, want %q", u.ConversationID, "override-conv")
	}
	if u.MessageID != "override-msg" {
		t.Fatalf("MessageID = %q, want %q", u.MessageID, "override-msg")
	}
}

// TestGenerate_RecordsError 验证失败时 Err 被填充且不记录结束原因。
func TestGenerate_RecordsError(t *testing.T) {
	model := &fakeModel{
		provider:    "test-provider",
		model:       "test-model",
		generateErr: errors.New("model exploded"),
	}
	recorder := &fakeRecorder{}

	a, err := New(WithModel(model), WithRecorder(recorder))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := a.Generate(context.Background(), fantasy.AgentCall{Prompt: "boom"}); err == nil {
		t.Fatal("expected Generate error, got nil")
	}

	u := recorder.Items()[0]
	if u.Err != "model exploded" {
		t.Fatalf("Err = %q, want %q", u.Err, "model exploded")
	}
	if u.FinishReason != "" {
		t.Fatalf("FinishReason = %q, want empty on error", u.FinishReason)
	}
}

// TestGenerate_NilRecorderNoop 验证未设置记录器时记录步骤直接早退（no-op），不影响回答返回。
func TestGenerate_NilRecorderNoop(t *testing.T) {
	model := &fakeModel{
		provider:  "test-provider",
		model:     "test-model",
		responses: []*fantasy.Response{stopResponse(1, 1)},
	}

	a, err := New(WithModel(model))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, err := a.Generate(context.Background(), fantasy.AgentCall{Prompt: "hello"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

// TestGenerate_RecordsToolCalls 验证多 step 场景下的工具调用计数。
func TestGenerate_RecordsToolCalls(t *testing.T) {
	model := &fakeModel{
		provider: "test-provider",
		model:    "test-model",
		responses: []*fantasy.Response{
			{
				Content: fantasy.ResponseContent{fantasy.ToolCallContent{
					ToolCallID: "call_1",
					ToolName:   "fake_tool",
					Input:      `{}`,
				}},
				FinishReason: fantasy.FinishReasonToolCalls,
				Usage:        fantasy.Usage{InputTokens: 10, OutputTokens: 5},
			},
			stopResponse(10, 5),
		},
	}
	recorder := &fakeRecorder{}

	a, err := New(
		WithModel(model),
		WithRecorder(recorder),
		WithTools(fantasy.NewAgentTool("fake_tool", "测试工具",
			func(_ context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
				return fantasy.NewTextResponse("ok"), nil
			},
		)),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, err := a.Generate(context.Background(), fantasy.AgentCall{Prompt: "do it"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(result.Steps))
	}

	u := recorder.Items()[0]
	if u.ToolCalls != 1 {
		t.Fatalf("ToolCalls = %d, want 1", u.ToolCalls)
	}
	if len(u.Steps) != 2 {
		t.Fatalf("steps len = %d, want 2", len(u.Steps))
	}
	if u.Steps[0].ToolCalls != 1 || u.Steps[1].ToolCalls != 0 {
		t.Fatalf("step tool calls = %d/%d, want 1/0", u.Steps[0].ToolCalls, u.Steps[1].ToolCalls)
	}
	if u.Total.TotalTokens != 30 {
		t.Fatalf("TotalTokens = %d, want 30", u.Total.TotalTokens)
	}
}

// TestStream_RecordsReasoningDuration 验证思考时长统计与用户回调链式调用。
func TestStream_RecordsReasoningDuration(t *testing.T) {
	model := &fakeModel{
		provider: "test-provider",
		model:    "test-model",
		streams:  []fantasy.StreamResponse{stopStream(fantasy.Usage{InputTokens: 10, OutputTokens: 20})},
	}
	recorder := &fakeRecorder{}

	a, err := New(WithModel(model), WithRecorder(recorder))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 用户回调：在思考开始与结束之间人为制造可观测的耗时，并记录调用痕迹
	var reasonStartCalled, reasonEndCalled bool
	call := fantasy.AgentStreamCall{
		Prompt: "hi",
		OnReasoningStart: func(id string, _ fantasy.ReasoningContent) error {
			reasonStartCalled = true
			time.Sleep(5 * time.Millisecond)
			return nil
		},
		OnReasoningEnd: func(id string, _ fantasy.ReasoningContent) error {
			reasonEndCalled = true
			return nil
		},
	}

	if _, err := a.Stream(context.Background(), call); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if !reasonStartCalled || !reasonEndCalled {
		t.Fatalf("user callbacks not chained: start=%v end=%v", reasonStartCalled, reasonEndCalled)
	}

	u := recorder.Items()[0]
	if u.Mode != "stream" {
		t.Fatalf("mode = %q, want stream", u.Mode)
	}
	if u.TotalDuration <= 0 {
		t.Fatalf("TotalDuration = %v, want > 0", u.TotalDuration)
	}
	if u.ReasoningDuration < 5*time.Millisecond {
		t.Fatalf("ReasoningDuration = %v, want >= 5ms", u.ReasoningDuration)
	}
	if u.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q, want stop", u.FinishReason)
	}
	if u.Total.InputTokens != 10 || u.Total.OutputTokens != 20 || u.Total.TotalTokens != 30 {
		t.Fatalf("total usage = %+v, want input=10 output=20 total=30", u.Total)
	}
}

// TestRecord_RecorderErrorIgnored 验证记录器失败只记日志，不影响回答返回。
func TestRecord_RecorderErrorIgnored(t *testing.T) {
	model := &fakeModel{
		provider:  "test-provider",
		model:     "test-model",
		responses: []*fantasy.Response{stopResponse(1, 1)},
	}
	recorder := &fakeRecorder{err: errors.New("db down")}

	a, err := New(WithModel(model), WithRecorder(recorder))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, err := a.Generate(context.Background(), fantasy.AgentCall{Prompt: "hello"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result despite recorder error")
	}
}
