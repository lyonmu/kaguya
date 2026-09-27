package system

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"charm.land/fantasy"
	agentruntime "github.com/lyonmu/kaguya/internal/agent/runtime"
	token "github.com/lyonmu/kaguya/internal/agent/token"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
)

func TestUsageRange(t *testing.T) {
	now := time.Date(2024, 12, 31, 12, 0, 0, 0, time.UTC)
	start, end, err := usageRange(&dtosystem.TokenUsageReq{}, now)
	if err != nil || start.Format(time.DateOnly) != "2024-01-01" || end.Format(time.DateOnly) != "2025-01-01" {
		t.Fatalf("%v %v %v", start, end, err)
	}
	for _, tc := range []struct {
		req        dtosystem.TokenUsageReq
		start, end int64
	}{
		{dtosystem.TokenUsageReq{EndTime: 1735689600}, 1704153600, 1735689601},
		{dtosystem.TokenUsageReq{StartTime: 1735603200}, 1735603200, 1735689600},
		{dtosystem.TokenUsageReq{StartTime: 1735689600, EndTime: 1735689600}, 1735689600, 1735689601},
	} {
		start, end, err := usageRange(&tc.req, now)
		if err != nil || start.Unix() != tc.start || end.Unix() != tc.end {
			t.Fatalf("defaults/seconds %+v: %v %v %v", tc, start, end, err)
		}
	}
	for _, req := range []dtosystem.TokenUsageReq{{StartTime: -1}, {EndTime: 1735689600000}, {StartTime: 1735776000, EndTime: 1735689600}, {StartTime: 1672531200, EndTime: 1735689600}} {
		if _, _, err := usageRange(&req, now); err != ErrInvalidUsageRange {
			t.Fatalf("accepted %+v: %v", req, err)
		}
	}
}

func TestUsageWindow(t *testing.T) {
	// 固定窗口：终点为当天 UTC 日末（秒级包含），起点为该日向前一年加一天的日初；
	// 是否跨闰日决定含首尾共 366 或 365 个自然日。
	for _, tc := range []struct {
		now        string
		start, end string
		days       int
	}{
		{"2026-09-12T05:21:00Z", "2025-09-13", "2026-09-12", 365},
		{"2024-12-31T12:00:00Z", "2024-01-01", "2024-12-31", 366},
	} {
		now, err := time.Parse(time.RFC3339, tc.now)
		if err != nil {
			t.Fatal(err)
		}
		start, end := usageWindow(now)
		if start.Format(time.DateOnly) != tc.start || end.Format(time.DateOnly) != tc.end {
			t.Fatalf("window %s: %v %v", tc.now, start, end)
		}
		if days := int(end.Truncate(24*time.Hour).Sub(start)/(24*time.Hour)) + 1; days != tc.days {
			t.Fatalf("window %s days=%d", tc.now, days)
		}
		// 默认请求时间段与活动窗口一致，只多出半开区间的一秒偏移。
		reqStart, reqEnd, err := usageRange(&dtosystem.TokenUsageReq{}, now)
		if err != nil || !reqStart.Equal(start) || !reqEnd.Equal(end.Add(time.Second)) {
			t.Fatalf("default range %s: %v %v %v", tc.now, reqStart, reqEnd, err)
		}
	}
}

func insertUsageTurn(ctx context.Context, t *testing.T, index int64, date time.Time, provider string, total int64) {
	t.Helper()
	_, err := db.EntClient.KaguyaChatTurn.Create().SetConversationID("usage").SetTurnIndex(index).
		SetUserContent("question").SetProviderID(provider).SetProviderName(provider).SetModelID("same-api-id").SetModelName("model").SetAPIProtocol("openai").
		SetStartedAt(date).SetFinishedAt(date).SetDurationMs(0).SetToolCalls(0).SetFinishReason("stop").
		SetInputTokens(total - 40).SetOutputTokens(30).SetReasoningTokens(10).SetCachedTokens(5).SetTotalTokens(total).SetMessages([]fantasy.Message{}).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
}

func insertUsageConversation(ctx context.Context, t *testing.T) {
	t.Helper()
	at := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := db.EntClient.KaguyaConversation.Create().SetID("usage").SetTitle("deleted conversation").SetModelID("model").SetModelName("model").SetLastMessageAt(at).SetDeletedAt(at).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
}

// 汇总卡片只统计请求时间段；活动日历固定为最近一年，不随请求时间段变化。
func TestTokenUsageSummaryFollowsRequestedRange(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}
	// 2025-01-01 ~ 2025-01-03（UTC，首尾秒包含）。
	req := &dtosystem.TokenUsageReq{StartTime: 1735689600, EndTime: 1735948799}
	insertUsageConversation(ctx, t)
	empty, err := svc.TokenUsage(ctx, req)
	if err != nil || empty.TotalTokens != 0 || empty.Conversations != 0 || len(empty.Days) == 0 || empty.Models == nil || empty.Providers == nil {
		t.Fatalf("empty=%+v %v", empty, err)
	}
	windowStart, windowEnd := usageWindow(time.Now())
	if empty.ActivityStart != windowStart.Format(time.DateOnly) || empty.ActivityEnd != windowEnd.Format(time.DateOnly) {
		t.Fatalf("activity window=%+v", empty)
	}
	at := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := int64(1); i <= 8; i++ {
		insertUsageTurn(ctx, t, i, at, fmt.Sprintf("provider-%d", i), i*100)
	}
	insertUsageTurn(ctx, t, 9, at.AddDate(0, 0, 1), "provider-8", 100)
	insertUsageTurn(ctx, t, 10, at.AddDate(0, 0, -1), "outside", 9999)
	insertUsageTurn(ctx, t, 11, at.AddDate(0, 0, 3), "outside", 9999)
	// 活动探针：今天与两年前各一条，只有固定窗口内的记录进入 Days。
	today := time.Now().UTC().Truncate(24 * time.Hour)
	insertUsageTurn(ctx, t, 12, today.Add(12*time.Hour), "activity", 500)
	insertUsageTurn(ctx, t, 13, today.AddDate(-2, 0, 0).Add(12*time.Hour), "expired", 7777)
	got, err := svc.TokenUsage(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalTokens != 3700 || got.Conversations != 1 || got.PeakTokens != 3600 || got.PeakTokensDate != "2025-01-01" || got.PeakConversations != 1 || got.PeakConversationsDate != "2025-01-01" {
		t.Fatalf("summary=%+v", got)
	}
	// Days 覆盖固定窗口（含今天在内 365 或 366 天）：末位是今天，空日补零，窗口外的记录不出现。
	last := len(got.Days) - 1
	if got.Days[last].Date != got.ActivityEnd || got.Days[last].TotalTokens != 500 || got.Days[last].Conversations != 1 {
		t.Fatalf("activity days=%+v", got.Days[last-5:])
	}
	if days := int(windowEnd.Truncate(24*time.Hour).Sub(windowStart)/(24*time.Hour)) + 1; len(got.Days) != days {
		t.Fatalf("activity length=%d want=%d", len(got.Days), days)
	}
	if got.Days[last-10].TotalTokens != 0 || got.Days[last-10].Conversations != 0 {
		t.Fatalf("activity day should be zero-filled: %+v", got.Days[last-10])
	}
	var activity int64
	for _, day := range got.Days {
		if day.TotalTokens == 7777 {
			t.Fatalf("expired turn leaked into activity: %+v", day)
		}
		activity += day.TotalTokens
	}
	if activity != 500 {
		t.Fatalf("activity total=%d", activity)
	}
}

// Token 构成固定统计全部历史：包含请求时间段之外的记录，不受时间段影响，且按用量倒序截断。
func TestTokenUsageCompositionIsAllTimeAndLimited(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}
	insertUsageConversation(ctx, t)
	at := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := int64(1); i <= 12; i++ {
		insertUsageTurn(ctx, t, i, at, fmt.Sprintf("provider-%d", i), i*100)
	}
	insertUsageTurn(ctx, t, 13, at.AddDate(0, 0, -1), "outside", 9999)
	// 每个模型使用独立 API ID，保留前十名截断的覆盖。
	if _, err := db.EntClient.ExecContext(ctx, "UPDATE kaguya_chat_turn SET model_id=provider_id"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.TokenUsage(ctx, &dtosystem.TokenUsageReq{StartTime: 1735689600, EndTime: 1735948799})
	if err != nil {
		t.Fatal(err)
	}
	// 时间段内只有 provider-1..12，共 7800；构成图覆盖全部历史 13 个分组，只取前 10。
	if got.TotalTokens != 7800 {
		t.Fatalf("summary=%+v", got)
	}
	if len(got.Models) != usageCompositionLimit || len(got.Providers) != usageCompositionLimit {
		t.Fatalf("composition=%+v %+v", got.Models, got.Providers)
	}
	if got.Models[0].ProviderID != "outside" || got.Models[0].TotalTokens != 9999 || got.Models[1].ProviderID != "provider-12" || got.Models[1].TotalTokens != 1200 || got.Providers[0].ID != "outside" {
		t.Fatalf("composition order=%+v %+v", got.Models, got.Providers)
	}
	for _, row := range got.Models {
		if row.ProviderID == "provider-1" {
			t.Fatalf("lowest usage leaked into truncated composition: %+v", row)
		}
	}
	narrow, err := svc.TokenUsage(ctx, &dtosystem.TokenUsageReq{StartTime: 1735689600, EndTime: 1735689600})
	if err != nil {
		t.Fatal(err)
	}
	if narrow.TotalTokens != 7800 || !reflect.DeepEqual(got.Models, narrow.Models) || !reflect.DeepEqual(got.Providers, narrow.Providers) {
		t.Fatalf("composition follows range: %+v", narrow)
	}
	for _, row := range got.Models {
		if row.InputTokens+row.OutputTokens+row.ReasoningTokens+row.CachedTokens != row.TotalTokens || row.OutputTokens != row.ReasoningTokens*2 {
			t.Fatalf("overlapping composition=%+v", row)
		}
	}
}

// 精确到秒筛选：结束秒内的亚秒记录计入，下一秒排除，不扩展到整日。
func TestTokenUsageSecondBoundary(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}
	insertUsageConversation(ctx, t)
	second := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	insertUsageTurn(ctx, t, 1, second.Add(-time.Nanosecond), "before", 100)
	insertUsageTurn(ctx, t, 2, second, "inside", 100)
	insertUsageTurn(ctx, t, 3, second.Add(999*time.Millisecond), "inside", 100)
	insertUsageTurn(ctx, t, 4, second.Add(time.Second), "after", 100)
	partial, err := svc.TokenUsage(ctx, &dtosystem.TokenUsageReq{StartTime: second.Unix(), EndTime: second.Unix()})
	if err != nil || partial.TotalTokens != 200 || partial.PeakTokens != 200 || partial.Start != "2025-01-01" || partial.End != "2025-01-01" || len(partial.Days) == 0 {
		t.Fatalf("second boundary: %+v %v", partial, err)
	}
}

// 实际模型 ID 是唯一聚合键：跨来源、提供商、配置记录和显示名称合并。
func TestTokenUsageCompositionMergesAPIModelIDs(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	insertUsageConversation(ctx, t)
	at := time.Now().UTC()
	for i, provider := range []string{"p1", "p2"} {
		insertUsageTurn(ctx, t, int64(i+1), at, provider, 100)
	}
	// 已软删除的配置仍可解析旧后台记录；记忆以调用时保存的 upstream ID 为准。
	if err := db.EntClient.KaguyaModelsInfo.Create().SetID("record-old").SetProviderID("p1").
		SetModelID("same-api-id").SetModelName("renamed").SetDeletedAt(at).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"record-old", "upstream:same-api-id"} {
		if err := db.EntClient.KaguyaTaskUsage.Create().SetKind("title").SetProviderID("p1").SetProviderName("p1").
			SetModelID(id).SetModelName(fmt.Sprintf("alias-%d", i)).SetFinishedAt(at).SetUsageKnown(true).
			SetInputTokens(20).SetOutputTokens(30).SetReasoningTokens(10).SetCachedTokens(5).SetTotalTokens(55).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.EntClient.KaguyaMemoryAttempt.Create().SetJobID("job").SetAttempt(1).SetResultCode("success").
		SetProviderID("p2").SetModelRecordID("missing-record").SetUpstreamModelID("same-api-id").SetUsageKnown(true).
		SetInputTokens(20).SetOutputTokens(30).SetReasoningTokens(10).SetCachedTokens(5).SetTotalTokens(55).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	recorder := NewTaskUsageRecorder(db.EntClient, "model_test", agentruntime.ProviderConfig{
		ProviderID: "p2", Name: "p2", ModelRecordID: "record-old", ModelID: "same-api-id", ModelName: "another name",
	})
	if err := recorder.RecordUsage(ctx, token.TurnUsage{FinishedAt: at, UsageKnown: true, Total: token.NormalizedUsage{
		InputTokens: 20, OutputTokens: 30, ReasoningTokens: 10, CacheHitTokens: 5, TotalTokens: 55,
	}}); err != nil {
		t.Fatal(err)
	}
	// 新调用不能因配置记录后来修改而改归其他模型。
	if err := db.EntClient.KaguyaModelsInfo.Create().SetID("record-new").SetProviderID("p2").
		SetModelID("other-api-id").SetModelName("model").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	other := NewTaskUsageRecorder(db.EntClient, "title", agentruntime.ProviderConfig{
		ProviderID: "p2", Name: "p2", ModelRecordID: "record-new", ModelID: "other-api-id", ModelName: "model",
	})
	if err := other.RecordUsage(ctx, token.TurnUsage{FinishedAt: at, UsageKnown: true, Total: token.NormalizedUsage{InputTokens: 10, TotalTokens: 10}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EntClient.ExecContext(ctx, "UPDATE kaguya_models_info SET model_id='changed' WHERE id='record-new'"); err != nil {
		t.Fatal(err)
	}
	// 未知消费不得计入构成。
	if err := db.EntClient.KaguyaTaskUsage.Create().SetKind("title").SetProviderID("p1").SetProviderName("p1").
		SetModelID("upstream:same-api-id").SetModelName("model").SetFinishedAt(at).SetUsageKnown(false).SetTotalTokens(999).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := (&SystemSvc{}).TokenUsage(ctx, &dtosystem.TokenUsageReq{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 2 {
		t.Fatalf("models=%+v", got.Models)
	}
	row := got.Models[0]
	if row.ID != "same-api-id" || row.TotalTokens != 420 || row.InputTokens != 210 || row.OutputTokens != 120 || row.ReasoningTokens != 60 || row.CachedTokens != 30 || row.ProviderID != "" || row.ProviderName != "" {
		t.Fatalf("merged model=%+v", row)
	}
	if got.Models[1].ID != "other-api-id" || got.Models[1].TotalTokens != 10 {
		t.Fatalf("distinct model=%+v", got.Models[1])
	}
	if len(got.Providers) != 2 || got.Providers[0].ID != "p2" || got.Providers[0].TotalTokens != 220 || got.Providers[1].TotalTokens != 210 {
		t.Fatalf("providers=%+v", got.Providers)
	}
	if got.TotalTokens != 430 {
		t.Fatalf("total=%d", got.TotalTokens)
	}
}

func TestUsageCompositionRanksAfterMerging(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	insertUsageConversation(ctx, t)
	at := time.Now().UTC()
	for i := int64(1); i <= 11; i++ {
		insertUsageTurn(ctx, t, i, at, fmt.Sprintf("p-%02d", i), 1000+i)
	}
	if _, err := db.EntClient.ExecContext(ctx, "UPDATE kaguya_chat_turn SET model_id=provider_id"); err != nil {
		t.Fatal(err)
	}
	// 两笔单独都排不进前十，合并后应该排第一。
	insertUsageTurn(ctx, t, 12, at, "p-a", 600)
	insertUsageTurn(ctx, t, 13, at, "p-b", 600)
	rows, err := usageComposition(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != usageCompositionLimit || rows[0].ID != "same-api-id" || rows[0].TotalTokens != 1200 || rows[9].ID != "p-03" {
		t.Fatalf("ranking before merge: %+v", rows)
	}
	seen := make(map[string]bool)
	for _, row := range rows {
		if seen[row.ID] {
			t.Fatalf("duplicate model: %s", row.ID)
		}
		seen[row.ID] = true
	}
}
