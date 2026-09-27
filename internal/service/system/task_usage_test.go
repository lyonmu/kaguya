package system

import (
	"context"
	"testing"
	"time"

	agentruntime "github.com/lyonmu/kaguya/internal/agent/runtime"
	token "github.com/lyonmu/kaguya/internal/agent/token"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
)

func TestUsageIncludesBackgroundAttemptsWithoutDuplicatingChat(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	at := time.Now().UTC().Truncate(24 * time.Hour).Add(time.Hour)
	insertUsageConversation(ctx, t)
	insertUsageTurn(ctx, t, 1, at, "provider", 100)
	recorder := NewTaskUsageRecorder(db.EntClient, "title", agentruntime.ProviderConfig{ProviderID: "provider", Name: "provider", ModelRecordID: "same-api-id", ModelName: "model", ConversationID: "usage"})
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := recorder.RecordUsage(canceled, token.TurnUsage{FinishedAt: at, UsageKnown: true, Total: token.NormalizedUsage{InputTokens: 30, OutputTokens: 20, ReasoningTokens: 10, TotalTokens: 50}}); err != nil {
		t.Fatal(err)
	}
	// 未返回用量不能当作零消费，也不能凭空估计。
	if err := recorder.RecordUsage(ctx, token.TurnUsage{FinishedAt: at}); err != nil {
		t.Fatal(err)
	}
	// 历史记忆的失败调用也产生了已知消费，关闭记忆不丢弃这部分。
	if err := db.EntClient.KaguyaMemoryAttempt.Create().SetJobID("old-job").SetAttempt(1).SetResultCode("provider_error").SetUsageKnown(true).
		SetProviderID("provider").SetModelRecordID("same-api-id").SetUpstreamModelID("model").SetCreatedAt(at).
		SetInputTokens(20).SetOutputTokens(50).SetReasoningTokens(40).SetTotalTokens(70).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	// TimeMixin 创建钩子使用当前时间；回归夹具显式构造历史本地时区记录。
	if _, err := db.EntClient.ExecContext(ctx, "UPDATE kaguya_memory_attempt SET created_at=?", at.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05 -0700 MST")); err != nil {
		t.Fatal(err)
	}
	// 已保存的失败聊天用量也应计入，但不增加成功会话计数。
	insertUsageTurn(ctx, t, 2, at, "provider", 80)
	if err := db.EntClient.KaguyaChatTurn.Update().Where().SetStatus("failed").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	// 恢复第一轮为 completed。
	turns, err := db.EntClient.KaguyaChatTurn.Query().All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range turns {
		if turn.TurnIndex == 1 {
			if err := turn.Update().SetStatus("completed").Exec(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := (&SystemSvc{}).TokenUsage(ctx, &dtosystem.TokenUsageReq{StartTime: at.Unix(), EndTime: at.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalTokens != 300 || got.BackgroundTokens != 120 || got.UnknownCalls != 1 || got.Conversations != 1 || got.PeakTokens != 300 {
		t.Fatalf("unexpected total=%d background=%d unknown=%d", got.TotalTokens, got.BackgroundTokens, got.UnknownCalls)
	}
	if len(got.Models) != 1 || got.Models[0].TotalTokens != 300 || len(got.Providers) != 1 || got.Providers[0].TotalTokens != 300 {
		t.Fatalf("composition differs: %+v", got)
	}
	row := got.Models[0]
	if row.InputTokens+row.OutputTokens+row.CachedTokens+row.ReasoningTokens != row.TotalTokens {
		t.Fatal("reasoning or cache was double-counted")
	}
	var activity int64
	for _, day := range got.Days {
		activity += day.TotalTokens
	}
	if activity != 300 {
		t.Fatalf("activity=%d", activity)
	}
	outside, err := (&SystemSvc{}).TokenUsage(ctx, &dtosystem.TokenUsageReq{StartTime: at.Add(time.Second).Unix(), EndTime: at.Add(time.Second).Unix()})
	if err != nil || outside.TotalTokens != 0 || outside.UnknownCalls != 0 || outside.Models[0].TotalTokens != 300 {
		t.Fatalf("range mismatch: %+v %v", outside, err)
	}
}
