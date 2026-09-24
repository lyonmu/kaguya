package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/agent/memorytools"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryjob"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
	memorysvc "github.com/lyonmu/kaguya/internal/service/memory"
	"go.uber.org/zap"
)

// msgText 读取消息文本（测试断言用）。
func msgText(m fantasy.Message) string {
	out := ""
	for _, part := range m.Content {
		if text, ok := part.(fantasy.TextPart); ok {
			out += text.Text
		}
	}
	return out
}

// makeAgentConversation 创建会话行（记忆模式可指定）。
func makeAgentConversation(t *testing.T, ctx context.Context, client *ent.Client, id, projectID string, mode kaguyaconversation.MemoryMode) {
	t.Helper()
	create := client.KaguyaConversation.Create().SetID(id).SetTitle("t").SetMemoryMode(mode).
		SetModelID("m").SetModelName("m").SetLastMessageAt(time.Now())
	if projectID != "" {
		create.SetProjectID(projectID)
	}
	if _, err := create.Save(ctx); err != nil {
		t.Fatal(err)
	}
}

// agentBlocks 构造一轮的展示块。
func agentBlocks(texts ...string) []dtochat.StoredBlock {
	blocks := make([]dtochat.StoredBlock, 0, len(texts))
	for i, text := range texts {
		blocks = append(blocks, dtochat.StoredBlock{
			Sequence: int64(i + 1), Type: dtochat.BlockTypeText, Text: text,
			StartedAt: time.Now(), FinishedAt: time.Now(),
			StartOrder: int64(i + 1), EndOrder: int64(i + 1),
		})
	}
	return blocks
}

// setupChatMemoryTest 在聊天测试库上启用真实记忆检索设施。
func setupChatMemoryTest(t *testing.T, enabled bool) (context.Context, *ent.Client) {
	t.Helper()
	ctx, client := setupChatTest(t)
	if err := db.EnsureMemoryFTS(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := memorysvc.EnsureSearchProjection(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.Update().
		SetMemoryEnabled(enabled).SetMemoryAutoCapture(enabled).SetMemoryContextTokens(2000).
		Exec(ctx); err != nil {
		t.Fatal(err)
	}
	return ctx, client
}

func createMemoryPage(t *testing.T, ctx context.Context, scope, title, body string) string {
	t.Helper()
	svc := memorysvc.NewService(db.EntClient, nil)
	detail, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: scope, Kind: "decision", Title: title, Summary: title, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	return detail.ID
}

// InjectMemory 把资料作为临时 user 消息插在系统消息之后，不改写历史底层数组。
func TestInjectMemory(t *testing.T) {
	messages := []fantasy.Message{
		fantasy.NewSystemMessage("system"),
		fantasy.NewUserMessage("hello"),
	}
	injected := InjectMemory(messages, "记忆资料")
	if len(injected) != 3 || len(messages) != 2 {
		t.Fatalf("injected=%d original=%d", len(injected), len(messages))
	}
	if msgText(injected[1]) != "记忆资料" || msgText(injected[2]) != "hello" {
		t.Fatalf("order: %+v", injected)
	}
	if got := InjectMemory(messages, ""); len(got) != 2 {
		t.Fatalf("empty text must not inject: %d", len(got))
	}
}

// 自动召回按会话范围绑定：项目对话不默认读取个人记忆，普通对话不读项目记忆；
// 关闭记忆时不注入也不注册工具。
func TestPrepareMemoryScopeBinding(t *testing.T) {
	ctx, client := setupChatMemoryTest(t, true)
	projectID := "p-scope"
	if err := client.KaguyaProject.Create().SetID(projectID).SetName("p").SetPath("/tmp/p-scope").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	createMemoryPage(t, ctx, memorysvc.ScopePersonal, "个人偏好", "个人内容：加密备份策略")
	createMemoryPage(t, ctx, "project:"+projectID, "项目约束", "项目内容：继续用 SQLCipher 加密")

	makeAgentConversation(t, ctx, client, "conv-personal", "", kaguyaconversation.MemoryModeInherit)
	personal := (&AgentSvc{}).prepareMemory(ctx, "conv-personal", 128000, "加密")
	if personal.unavailable || !strings.Contains(personal.text, "个人偏好") || strings.Contains(personal.text, "项目约束") {
		t.Fatalf("personal recall: %+v", personal)
	}
	if personal.reader == nil {
		t.Fatal("memory tools must be available for ordinary chat")
	}

	makeAgentConversation(t, ctx, client, "conv-project", projectID, kaguyaconversation.MemoryModeInherit)
	project := (&AgentSvc{}).prepareMemory(ctx, "conv-project", 128000, "加密")
	if strings.Contains(project.text, "个人偏好") || !strings.Contains(project.text, "项目约束") {
		t.Fatalf("project recall: %+v", project)
	}

	// 私密会话同时避免自动召回与记忆工具。
	makeAgentConversation(t, ctx, client, "conv-off", "", kaguyaconversation.MemoryModeOff)
	off := (&AgentSvc{}).prepareMemory(ctx, "conv-off", 128000, "加密")
	if off.enabled || off.text != "" || off.reader != nil {
		t.Fatalf("private conversation: %+v", off)
	}

	// 会话只读模式允许召回（工具一并保留），但不产生自动捕获。
	makeAgentConversation(t, ctx, client, "conv-readonly", "", kaguyaconversation.MemoryModeReadonly)
	readonly := (&AgentSvc{}).prepareMemory(ctx, "conv-readonly", 128000, "加密")
	if !readonly.enabled || readonly.text == "" {
		t.Fatalf("readonly conversation: %+v", readonly)
	}
}

// 自动召回失败是明确的非致命状态，不默默切成无记忆模式。
func TestPrepareMemoryDegraded(t *testing.T) {
	ctx, client := setupChatMemoryTest(t, true)
	createMemoryPage(t, ctx, memorysvc.ScopePersonal, "偏好", "内容")
	makeAgentConversation(t, ctx, client, "conv-degraded", "", kaguyaconversation.MemoryModeInherit)
	// 模拟索引损坏：检索失败必须降级并标记不可用。
	if _, err := client.ExecContext(ctx, "DROP TABLE kaguya_memory_fts"); err != nil {
		t.Fatal(err)
	}
	result := (&AgentSvc{}).prepareMemory(ctx, "conv-degraded", 128000, "偏好")
	if !result.unavailable {
		t.Fatalf("recall failure must surface: %+v", result)
	}
}

// completed 轮次事务写入来源待处理记录与召回记录；关闭自动捕获时不入队。
func TestSaveCompletedTurnCapturesMemory(t *testing.T) {
	ctx, client := setupChatMemoryTest(t, true)
	selection := &dtomemory.TurnMemorySelection{
		RetrieverVersion: memorysvc.RetrieverVersion, EstimatedTokens: 42,
		Refs: []dtomemory.TurnMemoryRef{{PageID: "page-x", Version: 3}},
	}
	if err := saveCompletedTurn(ctx, completedTurn{
		ConversationID: "conv-capture", ProjectID: "", Version: 0,
		UserContent: "记住这个决定", ProviderID: "p", ProviderName: "p",
		ModelID: "m", ModelName: "m", APIProtocol: "openai-chat",
		FinishReason: "stop", MemorySelection: selection,
		Blocks: agentBlocks("回答内容"),
	}); err != nil {
		t.Fatal(err)
	}
	src, err := client.KaguyaMemorySource.Query().Only(ctx)
	if err != nil || src.State != kaguyamemorysource.StatePending || src.ScopeKey != memorysvc.ScopePersonal {
		t.Fatalf("source=%+v err=%v", src, err)
	}
	turn, err := client.KaguyaChatTurn.Query().Only(ctx)
	if err != nil || turn.MemoryRefs.RetrieverVersion != memorysvc.RetrieverVersion || turn.MemoryRefs.EstimatedTokens != 42 {
		t.Fatalf("turn=%+v err=%v", turn.MemoryRefs, err)
	}

	// 关闭自动捕获：轮次照常保存，但不产生来源。
	if err := client.KaguyaSystemInfo.Update().SetMemoryAutoCapture(false).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := saveCompletedTurn(ctx, completedTurn{
		ConversationID: "conv-capture", Version: 1,
		UserContent: "第二轮", ProviderID: "p", ProviderName: "p",
		ModelID: "m", ModelName: "m", APIProtocol: "openai-chat", FinishReason: "stop",
		Blocks: agentBlocks("回答"),
	}); err != nil {
		t.Fatal(err)
	}
	if count, err := client.KaguyaMemorySource.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("sources=%d err=%v", count, err)
	}
}

// 会话删除撤销来源可用性并取消待处理作业；purge 一并清理派生内容。
func TestConversationDeleteRevokesMemory(t *testing.T) {
	ctx, client := setupChatMemoryTest(t, true)
	if err := saveCompletedTurn(ctx, completedTurn{
		ConversationID: "conv-revoke", Version: 0,
		UserContent: "待整理的内容", ProviderID: "p", ProviderName: "p",
		ModelID: "m", ModelName: "m", APIProtocol: "openai-chat", FinishReason: "stop",
		Blocks: agentBlocks("回答"),
	}); err != nil {
		t.Fatal(err)
	}
	src, err := client.KaguyaMemorySource.Query().Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaMemoryJob.Create().
		SetKind("compile").SetScopeKey(src.ScopeKey).SetConversationID("conv-revoke").
		SetInputSourceIds([]string{src.ID}).SetStatus("running").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := (&AgentSvc{}).ConversationDelete(ctx, "conv-revoke", true); err != nil {
		t.Fatal(err)
	}
	if count, err := client.KaguyaMemorySource.Query().Count(ctx); err != nil || count != 0 {
		t.Fatalf("purge kept sources: count=%d err=%v", count, err)
	}
	job, err := client.KaguyaMemoryJob.Query().Only(ctx)
	if err != nil || job.Status != kaguyamemoryjob.StatusCanceled {
		t.Fatalf("job=%+v err=%v", job, err)
	}
}

// 只读记忆工具满足窄接口：显式调用失败返回工具错误，不伪装无记忆。
func TestMemoryToolsContract(t *testing.T) {
	ctx, client := setupChatMemoryTest(t, true)
	createMemoryPage(t, ctx, memorysvc.ScopePersonal, "记忆存储", "项目要求继续用 SQLCipher")
	reader := memorysvc.NewScopedReader(memorysvc.NewService(client, zap.NewNop()), []string{memorysvc.ScopePersonal})
	tools := memorytools.Tools(reader)
	if len(tools) != 2 {
		t.Fatalf("tools=%d", len(tools))
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Info().Name] = true
	}
	if !names["memory_search"] || !names["memory_read"] {
		t.Fatalf("names=%v", names)
	}
	if toolNameConflict(tools, "memory_search") != true {
		t.Fatal("name conflict must be detected")
	}
	// scope 外读取返回工具错误文本。
	response, err := tools[1].Run(ctx, fantasy.ToolCall{ID: "1", Input: `{"page_id":"missing"}`})
	if err != nil || !response.IsError {
		t.Fatalf("out-of-scope read must be a tool error: %+v err=%v", response, err)
	}
}

// 压缩器为 Memory sidecar 预留空间：多 step 不重复累积，快照不含 sidecar。
func TestCompactorReservesMemorySidecar(t *testing.T) {
	compactor := &contextCompactor{window: 1000, percent: 90, maxOutput: 100, transientTokens: 200}
	// 无历史时估算含 sidecar；阈值为窗口 90%。
	_, prepared, err := compactor.prepare(context.Background(), fantasy.PrepareStepFunctionOptions{
		Messages: []fantasy.Message{fantasy.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Messages) != 1 {
		t.Fatalf("messages=%d", len(prepared.Messages))
	}
	// sidecar 占用计入阈值：窗口更小或 sidecar 更大时更早触发压缩/超限。
	small := &contextCompactor{window: 100, percent: 90, maxOutput: 10, transientTokens: 500}
	_, _, err = small.prepare(context.Background(), fantasy.PrepareStepFunctionOptions{
		Messages: []fantasy.Message{fantasy.NewUserMessage(strings.Repeat("x", 4000))},
	})
	if err == nil {
		t.Fatal("sidecar must reserve budget before threshold")
	}
}

// sidecar 只进入每次请求的临时消息：不进入 compactor 历史/快照，多 step 不重复累积。
func TestPrepareWithMemoryKeepsSidecarOutOfHistory(t *testing.T) {
	compactor := &contextCompactor{window: 100000, percent: 90, maxOutput: 100}
	render := func(context.Context) string { return "历史记忆资料" }
	prepare := prepareWithMemory(compactor, render)

	first := []fantasy.Message{fantasy.NewUserMessage("问题一")}
	_, result, err := prepare(context.Background(), fantasy.PrepareStepFunctionOptions{Messages: first})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 2 || msgText(result.Messages[0]) != "历史记忆资料" {
		t.Fatalf("sidecar not injected: %+v", result.Messages)
	}
	second := append(append([]fantasy.Message{}, first...), fantasy.NewUserMessage("问题二"))
	_, result2, err := prepare(context.Background(), fantasy.PrepareStepFunctionOptions{Messages: second})
	if err != nil {
		t.Fatal(err)
	}
	// 第二步只附加一条 sidecar，不随 step 数量累积。
	if len(result2.Messages) != 3 {
		t.Fatalf("sidecar accumulated across steps: %d", len(result2.Messages))
	}
	// compactor 历史与快照不含 sidecar 副本。
	for _, message := range compactor.messages {
		if strings.Contains(msgText(message), "历史记忆资料") {
			t.Fatalf("sidecar leaked into compactor history: %+v", compactor.messages)
		}
	}
	if strings.Contains(fmt.Sprint(compactor.snapshot(&fantasy.AgentResult{})), "历史记忆资料") {
		t.Fatal("sidecar leaked into compaction snapshot")
	}
	if compactor.seen != 2 {
		t.Fatalf("compactor seen=%d", compactor.seen)
	}
}
