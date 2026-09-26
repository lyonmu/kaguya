package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	agentruntime "github.com/lyonmu/kaguya/internal/agent/runtime"
	token "github.com/lyonmu/kaguya/internal/agent/token"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	"github.com/lyonmu/kaguya/internal/global"
	initialize "github.com/lyonmu/kaguya/internal/init"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
	"go.uber.org/zap"
)

type memoryTestID struct{ value atomic.Int64 }

func (g *memoryTestID) GenID() (int64, error) { return g.value.Add(1), nil }

// sharedTestID 同一测试进程内共享 ID 序列，多 fixture 同库/跨库都不重复。
var sharedTestID = &memoryTestID{value: atomic.Int64{}}

func init() { sharedTestID.value.Store(100000000000000) }

// setupMemoryTest 建立真实 FTS 设施的内存测试库；插入依赖雪花 ID 的记录前
// 已初始化 global.Id，避免并行测试污染共享状态。
var testDBSeq atomic.Int64

func setupMemoryTest(t *testing.T) (context.Context, *Service, *ent.Client) {
	t.Helper()
	// 每次装配独立内存库：同测试内的多 fixture 不共享数据。
	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared&_foreign_keys=on", t.Name(), testDBSeq.Add(1))
	conn, err := sql.Open(dialect.SQLite, dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, conn)))
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldID, oldLogger := global.Id, global.Logger
	global.Id, global.Logger = sharedTestID, zap.NewNop()
	t.Cleanup(func() { global.Id, global.Logger = oldID, oldLogger })
	if err := initialize.Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	// 测试库不经 db.InitSQLite：显式迁移 FTS 设施与搜索投影。
	if err := db.EnsureMemoryFTS(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSearchProjection(ctx, client); err != nil {
		t.Fatal(err)
	}
	svc := NewService(client, zap.NewNop())
	return ctx, svc, client
}

// fakeCaller 按调用序返回预设结果，记录每次调用的提示词与输出预算。
type fakeCaller struct {
	mu      atomic.Int64
	prompts []string
	steps   []func(callIndex int) (CallResult, error)
}

func (f *fakeCaller) Call(_ context.Context, _ agentruntime.ProviderConfig, systemPrompt, payload string, _ int64) (CallResult, error) {
	index := int(f.mu.Add(1)) - 1
	f.prompts = append(f.prompts, systemPrompt+"\n"+payload)
	if index >= len(f.steps) {
		return CallResult{}, fmt.Errorf("fake caller: unexpected call %d", index)
	}
	return f.steps[index](index)
}

func okResult(raw string) CallResult {
	return CallResult{Text: raw, Usage: token.NormalizedUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}, UsageKnown: true, FinishReason: "stop"}
}

func extractJSON(candidates ...dtomemory.Candidate) string {
	if candidates == nil {
		candidates = []dtomemory.Candidate{}
	}
	return fmt.Sprintf(`{"schema_version":1,"candidates":%s}`, mustJSON(candidates))
}

func planJSON(changes ...dtomemory.PagePatch) string {
	if changes == nil {
		changes = []dtomemory.PagePatch{}
	}
	return fmt.Sprintf(`{"schema_version":1,"changes":%s}`, mustJSON(changes))
}

func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// setupPolicy 开启全局记忆与自动捕获，返回当前策略版本。
func setupPolicy(t *testing.T, ctx context.Context, client *ent.Client, enabled, autoCapture bool) Policy {
	t.Helper()
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).
		SetMemoryEnabled(enabled).SetMemoryAutoCapture(autoCapture).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	policy, err := LoadPolicy(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

var projectSeq atomic.Int64

// makeProject 创建测试项目并返回 ID；路径唯一，避免同库多项目冲突。
func makeProject(t *testing.T, ctx context.Context, client *ent.Client, name string) string {
	t.Helper()
	seq := projectSeq.Add(1)
	id := fmt.Sprintf("project-%s-%d", name, seq)
	if err := client.KaguyaProject.Create().SetID(id).SetName(id).SetPath(fmt.Sprintf("/tmp/%s", id)).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	return id
}

var conversationSeq atomic.Int64

// makeConversation 创建会话行；同库多场景时 ID 唯一并返回实际 ID。
func makeConversation(t *testing.T, ctx context.Context, client *ent.Client, id, projectID string, mode kaguyaconversation.MemoryMode) string {
	t.Helper()
	id = fmt.Sprintf("%s-%d", id, conversationSeq.Add(1))
	create := client.KaguyaConversation.Create().SetID(id).SetTitle("t").SetMemoryMode(mode).
		SetModelID("m").SetModelName("m").SetLastMessageAt(time.Now())
	if projectID != "" {
		create.SetProjectID(projectID)
	}
	if _, err := create.Save(ctx); err != nil {
		t.Fatal(err)
	}
	return id
}

// makeTurn 创建 completed 轮次与展示块，模拟 saveCompletedTurn 落库结果。
func makeTurn(t *testing.T, ctx context.Context, client *ent.Client, conversationID, userContent string, answers ...string) string {
	t.Helper()
	last, err := client.KaguyaChatTurn.Query().
		Where(kaguyachatturn.ConversationIDEQ(conversationID)).
		Order(ent.Desc(kaguyachatturn.FieldTurnIndex)).First(ctx)
	index := int64(1)
	if err == nil {
		index = last.TurnIndex + 1
	} else if !ent.IsNotFound(err) {
		t.Fatal(err)
	}
	row, err := client.KaguyaChatTurn.Create().
		SetConversationID(conversationID).SetTurnIndex(index).SetStatus(kaguyachatturn.StatusCompleted).
		SetUserContent(userContent).
		SetProviderID("p").SetProviderName("p").SetModelID("m").SetModelName("m").SetAPIProtocol("openai-chat").
		SetStartedAt(time.Now()).SetFinishedAt(time.Now()).SetDurationMs(1).SetToolCalls(0).
		SetFinishReason("stop").SetInputTokens(1).SetOutputTokens(1).SetTotalTokens(2).
		SetCachedTokens(0).SetReasoningTokens(0).SetMessages([]fantasy.Message{fantasy.NewUserMessage(userContent)}).
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sequence := int64(0)
	for _, answer := range answers {
		sequence++
		if err := client.KaguyaChatBlock.Create().SetTurnID(row.ID).SetSequence(sequence).
			SetType("text").SetText(answer).SetToolCallID("").SetToolName("").SetInput("").
			SetProviderExecuted(false).SetIsError(false).SetErrorMessage("").
			SetStartedAt(time.Now()).SetFinishedAt(time.Now()).SetStartOrder(sequence).SetEndOrder(sequence).
			Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return row.ID
}

// captureTurn 模拟 saveCompletedTurn 的捕获步骤（含策略门禁）。
func captureTurn(t *testing.T, ctx context.Context, client *ent.Client, conversationID, turnID string) {
	t.Helper()
	policy, err := LoadPolicy(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.AutoCapture {
		return
	}
	if err := CaptureCompletedTx(ctx, client, CaptureInput{
		ConversationID: conversationID, TurnID: turnID, PolicyEpoch: policy.Epoch,
	}); err != nil {
		t.Fatal(err)
	}
}

// backdateSources 把待处理来源的时间前移，满足防抖/最长等待条件。
func backdateSources(t *testing.T, ctx context.Context, client *ent.Client, age time.Duration) {
	t.Helper()
	if err := client.KaguyaMemorySource.Update().SetCapturedAt(time.Now().Add(-age)).Exec(ctx); err != nil {
		t.Fatal(err)
	}
}

// fakeTaskModel 返回固定的任务模型快照。
func fakeTaskModel() *servicesystem.TaskModel {
	return &servicesystem.TaskModel{
		Config:               agentruntime.ProviderConfig{ModelID: "fake", Name: "fake"},
		ModelRecordID:        "record-1",
		ProviderID:           "provider-1",
		UpstreamModelID:      "fake",
		TokenContextWindow:   128000,
		TokenMaxOutputTokens: 8192,
	}
}

// memoryServiceWith 构造使用 fake caller 与 fake 任务模型解析的服务。
func memoryServiceWith(svc *Service, caller *fakeCaller) *Service {
	return svc.WithCaller(caller).WithTaskModelResolver(
		func(context.Context, *ent.Client, string) (*servicesystem.TaskModel, error) {
			return fakeTaskModel(), nil
		})
}

// pageBody 生成带唯一主题的测试正文。
func pageBody(marker string) string {
	return fmt.Sprintf("## 决定\n%s\n\n## 原因\n测试依据。", marker)
}

// ptrString 返回字符串指针，用于可选编辑字段。
func ptrString(value string) *string { return &value }
