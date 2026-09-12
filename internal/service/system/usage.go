package system

import (
	"context"
	"errors"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
)

var ErrInvalidUsageRange = errors.New("invalid token usage date range")

// usageCompositionLimit 为模型/厂商构成返回的最大分组数，按总用量倒序取前 N 项；
// 未进入前 N 项的历史用量仍计入汇总卡片的总量。
const usageCompositionLimit = 10

// usageWindow 返回固定的活动统计窗口：结束为 now 所在 UTC 自然日末（秒级包含），
// 开始为该日向前一年再加一天的日初，是否跨闰日决定含首尾共 366 或 365 个自然日。
func usageWindow(now time.Time) (time.Time, time.Time) {
	end := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, 1).Add(-time.Second)
	return end.Truncate(24*time.Hour).AddDate(-1, 0, 1), end
}

// usageRange 解析请求时间段，返回首尾秒均包含的 [start, end)；未指定时使用 usageWindow 的默认窗口。
func usageRange(req *dtosystem.TokenUsageReq, now time.Time) (time.Time, time.Time, error) {
	if err := dtosystem.ValidateTimeRange(req.StartTime, req.EndTime); err != nil {
		return time.Time{}, time.Time{}, ErrInvalidUsageRange
	}
	_, end := usageWindow(now)
	if req.EndTime != 0 {
		end = time.Unix(req.EndTime, 0).UTC()
	}
	// 只给出结束时间时，起点仍按该结束日向前一年加一天推算。
	start := end.Truncate(24*time.Hour).AddDate(-1, 0, 1)
	if req.StartTime != 0 {
		start = time.Unix(req.StartTime, 0).UTC()
	}
	if start.After(end) || end.Truncate(24*time.Hour).Sub(start.Truncate(24*time.Hour)) > 365*24*time.Hour {
		return time.Time{}, time.Time{}, ErrInvalidUsageRange
	}
	// 半开区间覆盖结束秒内的亚秒记录，不把秒级结束值错误地当作整日。
	return start, end.Add(time.Second), nil
}

// TokenUsage 仅统计完整保存的聊天轮次，软删除不抹除历史消耗；不读取 messages/blocks。
// 请求时间段只影响汇总卡片；活动日历固定反映最近一年，模型/厂商构成固定统计全部历史。
// 会话数按 conversation_id 去重，日会话数指当天有成功问答的会话数。
func (s *SystemSvc) TokenUsage(ctx context.Context, req *dtosystem.TokenUsageReq) (*dtosystem.TokenUsageResp, error) {
	now := time.Now()
	start, end, err := usageRange(req, now)
	if err != nil {
		return nil, err
	}
	resp := &dtosystem.TokenUsageResp{Start: start.Format(time.DateOnly), End: end.Add(-time.Second).Format(time.DateOnly)}
	query := db.EntClient.KaguyaChatTurn.Query().Where(kaguyachatturn.StatusEQ(kaguyachatturn.StatusCompleted), kaguyachatturn.FinishedAtGTE(start), kaguyachatturn.FinishedAtLT(end))
	// 汇总卡片：日峰值需要按 UTC 自然日聚合后取最大，会话数在整段时间段内去重。
	days, err := usageDays(ctx, query.Clone())
	if err != nil {
		return nil, err
	}
	for _, day := range days {
		resp.TotalTokens += day.TotalTokens
		if day.TotalTokens > resp.PeakTokens {
			resp.PeakTokens, resp.PeakTokensDate = day.TotalTokens, day.Date
		}
		if day.Conversations > resp.PeakConversations {
			resp.PeakConversations, resp.PeakConversationsDate = day.Conversations, day.Date
		}
	}
	var counts []struct {
		Conversations int64 `json:"conversations"`
	}
	err = query.Clone().Modify(func(s *sql.Selector) {
		s.Select(sql.As(sql.Count("DISTINCT "+s.C(kaguyachatturn.FieldConversationID)), "conversations"))
	}).Scan(ctx, &counts)
	if err != nil {
		return nil, err
	}
	if len(counts) > 0 {
		resp.Conversations = counts[0].Conversations
	}
	// 活动日历固定为最近一年（末位为今天，含首尾 365 或 366 天），不随请求时间段变化。
	activityStart, activityEnd := usageWindow(now)
	resp.ActivityStart, resp.ActivityEnd = activityStart.Format(time.DateOnly), activityEnd.Format(time.DateOnly)
	activityDays, err := usageDays(ctx, db.EntClient.KaguyaChatTurn.Query().Where(
		kaguyachatturn.StatusEQ(kaguyachatturn.StatusCompleted),
		kaguyachatturn.FinishedAtGTE(activityStart), kaguyachatturn.FinishedAtLT(activityEnd.Add(time.Second))))
	if err != nil {
		return nil, err
	}
	byDate := make(map[string]dtosystem.TokenUsageDay, len(activityDays))
	for _, day := range activityDays {
		byDate[day.Date] = day
	}
	resp.Days = make([]dtosystem.TokenUsageDay, 0, 366)
	// activityStart 与逐日推进都落在 UTC 日初，活动日末（当天 23:59:59）本身也会被包含。
	for date := activityStart; date.Before(activityEnd); date = date.AddDate(0, 0, 1) {
		key := date.Format(time.DateOnly)
		day := byDate[key]
		day.Date = key
		resp.Days = append(resp.Days, day)
	}
	// Token 构成固定统计全部历史（从开始记录到现在），不随请求时间段变化；只含完整提交的轮次。
	all := db.EntClient.KaguyaChatTurn.Query().Where(kaguyachatturn.StatusEQ(kaguyachatturn.StatusCompleted))
	resp.Models, err = usageComposition(ctx, all.Clone(), true)
	if err != nil {
		return nil, err
	}
	resp.Providers, err = usageComposition(ctx, all.Clone(), false)
	return resp, err
}

// usageDays 按 UTC 自然日聚合 [start, end) 内的 Token 用量与去重会话数，仅返回有记录的日期。
func usageDays(ctx context.Context, query *ent.KaguyaChatTurnQuery) ([]dtosystem.TokenUsageDay, error) {
	days := make([]dtosystem.TokenUsageDay, 0)
	err := query.Modify(func(s *sql.Selector) {
		column := s.C(kaguyachatturn.FieldFinishedAt)
		// 历史 SQLite 时间字段保存 Go 时间字符串（含时区名称），SQLite DATE 无法解析该格式。
		date := "SUBSTR(" + column + ", 1, 10)"
		s.Select(sql.As(date, "date"), sql.As(sql.Sum(s.C(kaguyachatturn.FieldTotalTokens)), "total_tokens"), sql.As(sql.Count("DISTINCT "+s.C(kaguyachatturn.FieldConversationID)), "conversations")).GroupBy(date).OrderBy(date)
	}).Scan(ctx, &days)
	if err != nil {
		return nil, err
	}
	return days, nil
}

func usageComposition(ctx context.Context, query *ent.KaguyaChatTurnQuery, model bool) ([]dtosystem.TokenUsageComposition, error) {
	rows := make([]dtosystem.TokenUsageComposition, 0)
	err := query.Modify(func(s *sql.Selector) {
		id, name := kaguyachatturn.FieldProviderID, kaguyachatturn.FieldProviderName
		if model {
			id, name = kaguyachatturn.FieldModelID, kaguyachatturn.FieldModelName
		}
		fields := []string{sql.As(s.C(id), "id"), sql.As(sql.Max(s.C(name)), "name"), s.C(kaguyachatturn.FieldProviderID), sql.As(sql.Max(s.C(kaguyachatturn.FieldProviderName)), "provider_name")}
		for _, field := range []string{kaguyachatturn.FieldInputTokens, kaguyachatturn.FieldOutputTokens, kaguyachatturn.FieldReasoningTokens, kaguyachatturn.FieldCachedTokens, kaguyachatturn.FieldTotalTokens} {
			fields = append(fields, sql.As(sql.Sum(s.C(field)), field))
		}
		group := []string{s.C(kaguyachatturn.FieldProviderID)}
		if model {
			group = append(group, s.C(id))
		}
		s.Select(fields...).GroupBy(group...).OrderBy(sql.Desc("total_tokens"), s.C(kaguyachatturn.FieldProviderID), s.C(id)).Limit(usageCompositionLimit)
	}).Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		// 思考包含在输出中；缓存读取独立于 Fantasy 输入。缓存写入归入输入，保持四段总和与总用量一致。
		row := &rows[i]
		row.CachedTokens = min(row.CachedTokens, row.TotalTokens)
		output := min(row.OutputTokens, row.TotalTokens-row.CachedTokens)
		row.ReasoningTokens = min(row.ReasoningTokens, output)
		row.OutputTokens = output - row.ReasoningTokens
		row.InputTokens = row.TotalTokens - row.CachedTokens - output
	}
	return rows, nil
}
