package system

import (
	"fmt"
	"testing"
	"time"

	"charm.land/fantasy"
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

func TestTokenUsageAggregatesAndLimits(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	client := db.EntClient
	svc := &SystemSvc{}
	req := &dtosystem.TokenUsageReq{StartTime: 1735689600, EndTime: 1735948799}
	empty, err := svc.TokenUsage(ctx, req)
	if err != nil || empty.TotalTokens != 0 || len(empty.Days) != 3 || empty.Models == nil || empty.Providers == nil {
		t.Fatalf("empty=%+v %v", empty, err)
	}
	at := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err = client.KaguyaConversation.Create().SetID("usage").SetTitle("deleted conversation").SetModelID("model").SetModelName("model").SetLastMessageAt(at).SetDeletedAt(at).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(index int64, date time.Time, provider string, total int64) {
		t.Helper()
		_, err := client.KaguyaChatTurn.Create().SetConversationID("usage").SetTurnIndex(index).
			SetUserContent("question").SetProviderID(provider).SetProviderName(provider).SetModelID("same-api-id").SetModelName("model").SetAPIProtocol("openai").
			SetStartedAt(date).SetFinishedAt(date).SetDurationMs(0).SetToolCalls(0).SetFinishReason("stop").
			SetInputTokens(total - 40).SetOutputTokens(30).SetReasoningTokens(10).SetCachedTokens(5).SetTotalTokens(total).SetMessages([]fantasy.Message{}).Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := int64(1); i <= 8; i++ {
		insert(i, at, fmt.Sprintf("provider-%d", i), i*100)
	}
	insert(9, at.AddDate(0, 0, 1), "provider-8", 100)
	insert(10, at.AddDate(0, 0, -1), "outside", 9999)
	insert(11, at.AddDate(0, 0, 3), "outside", 9999)
	got, err := svc.TokenUsage(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalTokens != 3700 || got.Conversations != 1 || got.PeakTokens != 3600 || got.PeakTokensDate != "2025-01-01" || got.PeakConversations != 1 {
		t.Fatalf("summary=%+v", got)
	}
	if got.Days[0].Conversations != 1 || got.Days[1].TotalTokens != 100 || got.Days[2].TotalTokens != 0 {
		t.Fatalf("days=%+v", got.Days)
	}
	if len(got.Models) != 6 || len(got.Providers) != 6 || got.Models[0].ProviderID != "provider-8" || got.Models[0].TotalTokens != 900 {
		t.Fatalf("composition=%+v", got.Models)
	}
	// 精确到秒筛选：结束秒内的亚秒记录计入，下一秒排除，不扩展到整日。
	second := at.Add(12 * time.Hour)
	insert(12, second.Add(-time.Nanosecond), "before", 100)
	insert(13, second, "inside", 100)
	insert(14, second.Add(999*time.Millisecond), "inside", 100)
	insert(15, second.Add(time.Second), "after", 100)
	partial, err := svc.TokenUsage(ctx, &dtosystem.TokenUsageReq{StartTime: second.Unix(), EndTime: second.Unix()})
	if err != nil || partial.TotalTokens != 200 || len(partial.Days) != 1 || partial.Days[0].TotalTokens != 200 {
		t.Fatalf("second boundary: %+v %v", partial, err)
	}
	for _, row := range got.Models {
		if row.InputTokens+row.OutputTokens+row.ReasoningTokens+row.CachedTokens != row.TotalTokens || row.OutputTokens != row.ReasoningTokens*2 {
			t.Fatalf("overlapping composition=%+v", row)
		}
	}
}
