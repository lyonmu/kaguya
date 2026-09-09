package system

import (
	"context"
	"errors"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
)

var ErrInvalidUsageRange = errors.New("invalid token usage date range")

func usageRange(req *dtosystem.TokenUsageReq, now time.Time) (time.Time, time.Time, error) {
	if err := dtosystem.ValidateTimeRange(req.StartTime, req.EndTime); err != nil {
		return time.Time{}, time.Time{}, ErrInvalidUsageRange
	}
	end := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, 1).Add(-time.Second)
	if req.EndTime != 0 {
		end = time.Unix(req.EndTime, 0).UTC()
	}
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
// 会话数按 conversation_id 去重，日会话数指当天有成功问答的会话数。
func (s *SystemSvc) TokenUsage(ctx context.Context, req *dtosystem.TokenUsageReq) (*dtosystem.TokenUsageResp, error) {
	start, end, err := usageRange(req, time.Now())
	if err != nil {
		return nil, err
	}
	query := db.EntClient.KaguyaChatTurn.Query().Where(kaguyachatturn.FinishedAtGTE(start), kaguyachatturn.FinishedAtLT(end))
	var days []dtosystem.TokenUsageDay
	err = query.Clone().Modify(func(s *sql.Selector) {
		column := s.C(kaguyachatturn.FieldFinishedAt)
		date := "DATE(" + column + ")"
		switch s.Dialect() {
		case dialect.SQLite:
			// modernc 保存 Go 时间字符串（含时区名称），SQLite DATE 无法解析该格式。
			date = "SUBSTR(" + column + ", 1, 10)"
		case dialect.MySQL:
			// 返回字符串，避免 parseTime 将 DATE 扫描为 RFC3339 而无法匹配自然日。
			date = "DATE_FORMAT(" + column + ", '%Y-%m-%d')"
		case dialect.Postgres:
			date = "TO_CHAR(" + column + ", 'YYYY-MM-DD')"
		}
		s.Select(sql.As(date, "date"), sql.As(sql.Sum(s.C(kaguyachatturn.FieldTotalTokens)), "total_tokens"), sql.As(sql.Count("DISTINCT "+s.C(kaguyachatturn.FieldConversationID)), "conversations")).GroupBy(date).OrderBy(date)
	}).Scan(ctx, &days)
	if err != nil {
		return nil, err
	}
	resp := &dtosystem.TokenUsageResp{Start: start.Format(time.DateOnly), End: end.Add(-time.Second).Format(time.DateOnly), Days: make([]dtosystem.TokenUsageDay, 0)}
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
	byDate := make(map[string]dtosystem.TokenUsageDay, len(days))
	for _, day := range days {
		byDate[day.Date] = day
	}
	for date := start.Truncate(24 * time.Hour); date.Before(end); date = date.AddDate(0, 0, 1) {
		key := date.Format(time.DateOnly)
		day := byDate[key]
		day.Date = key
		resp.Days = append(resp.Days, day)
		resp.TotalTokens += day.TotalTokens
		if day.TotalTokens > resp.PeakTokens {
			resp.PeakTokens, resp.PeakTokensDate = day.TotalTokens, key
		}
		if day.Conversations > resp.PeakConversations {
			resp.PeakConversations, resp.PeakConversationsDate = day.Conversations, key
		}
	}
	resp.Models, err = usageComposition(ctx, query.Clone(), true)
	if err != nil {
		return nil, err
	}
	resp.Providers, err = usageComposition(ctx, query.Clone(), false)
	if err != nil {
		return nil, err
	}
	return resp, nil
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
		s.Select(fields...).GroupBy(group...).OrderBy(sql.Desc("total_tokens"), s.C(kaguyachatturn.FieldProviderID), s.C(id)).Limit(6)
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
