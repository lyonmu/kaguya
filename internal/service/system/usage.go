package system

import (
	"context"
	"errors"
	"time"

	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
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

// usageLedger 合并既有聊天、记忆调用与新增后台计量；不复制历史数据，避免重复计费。
// 未完成聊天仅计入已落库的已知消费，会话数仍表示成功会话，不被后台任务抬高。
// model_id 统一为实际 API ID；旧后台记录通过配置表解析（含软删除），无法解析时保留原 ID。
// 时间兼容 SQLCipher RFC3339 offset 和历史 Go time.String；先截取整秒避免 SQLite 将 .999999999 四舍五入到下一秒。
const usageLedger = `WITH usage_raw AS (
 SELECT COALESCE(finished_at, updated_at) AS at,
 CASE WHEN status='completed' THEN conversation_id ELSE NULL END AS conversation_id,
 provider_id,provider_name,model_id,model_name,input_tokens,output_tokens,reasoning_tokens,cached_tokens,total_tokens,
 'chat' AS kind,1 AS known FROM kaguya_chat_turn WHERE status='completed' OR total_tokens>0
 UNION ALL
 SELECT a.created_at,NULL,a.provider_id,COALESCE(p.provider_name,a.provider_id),
 COALESCE(NULLIF(a.upstream_model_id,''),m.model_id,a.model_record_id),COALESCE(m.model_name,a.upstream_model_id),
 a.input_tokens,a.output_tokens,a.reasoning_tokens,a.cached_tokens,a.total_tokens,'memory',a.usage_known
 FROM kaguya_memory_attempt a LEFT JOIN kaguya_provider_info p ON p.id=a.provider_id
 LEFT JOIN kaguya_models_info m ON m.id=a.model_record_id
 UNION ALL
 SELECT t.finished_at,NULL,t.provider_id,t.provider_name,
 CASE WHEN t.model_id LIKE 'upstream:%' THEN SUBSTR(t.model_id,10) ELSE COALESCE(m.model_id,t.model_id) END,
 t.model_name,t.input_tokens,t.output_tokens,t.reasoning_tokens,t.cached_tokens,t.total_tokens,t.kind,t.usage_known
 FROM kaguya_task_usage t LEFT JOIN kaguya_models_info m ON m.id=t.model_id
), usage AS (
 SELECT *,CAST(strftime('%s',SUBSTR(at,1,19)||
 CASE WHEN INSTR(at,' +')>0 THEN SUBSTR(at,INSTR(at,' +')+1,3)||':'||SUBSTR(at,INSTR(at,' +')+4,2)
 WHEN INSTR(at,' -')>0 THEN SUBSTR(at,INSTR(at,' -')+1,3)||':'||SUBSTR(at,INSTR(at,' -')+4,2)
 WHEN SUBSTR(at,-6,1) IN ('+','-') AND SUBSTR(at,-3,1)=':' THEN SUBSTR(at,-6)
 ELSE '+00:00' END) AS INTEGER) AS epoch FROM usage_raw
) `

// TokenUsage 的总量、峰值、日历与模型/厂商构成采用同一记账口径。
func (s *SystemSvc) TokenUsage(ctx context.Context, req *dtosystem.TokenUsageReq) (*dtosystem.TokenUsageResp, error) {
	now := time.Now()
	start, end, err := usageRange(req, now)
	if err != nil {
		return nil, err
	}
	resp := &dtosystem.TokenUsageResp{Start: start.Format(time.DateOnly), End: end.Add(-time.Second).Format(time.DateOnly)}
	days, err := usageDays(ctx, start, end)
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
	rows, err := db.EntClient.QueryContext(ctx, usageLedger+`SELECT COUNT(DISTINCT conversation_id),
 COALESCE(SUM(CASE WHEN kind<>'chat' AND known=1 THEN total_tokens ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN known=0 THEN 1 ELSE 0 END),0) FROM usage WHERE epoch>=? AND epoch<?`, start.Unix(), end.Unix())
	if err != nil {
		return nil, err
	}
	if rows.Next() {
		err = rows.Scan(&resp.Conversations, &resp.BackgroundTokens, &resp.UnknownCalls)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	activityStart, activityEnd := usageWindow(now)
	resp.ActivityStart, resp.ActivityEnd = activityStart.Format(time.DateOnly), activityEnd.Format(time.DateOnly)
	activity, err := usageDays(ctx, activityStart, activityEnd.Add(time.Second))
	if err != nil {
		return nil, err
	}
	byDate := make(map[string]dtosystem.TokenUsageDay, len(activity))
	for _, day := range activity {
		byDate[day.Date] = day
	}
	resp.Days = make([]dtosystem.TokenUsageDay, 0, 366)
	for date := activityStart; !date.After(activityEnd); date = date.AddDate(0, 0, 1) {
		key := date.Format(time.DateOnly)
		day := byDate[key]
		day.Date = key
		resp.Days = append(resp.Days, day)
	}
	resp.Models, err = usageComposition(ctx, true)
	if err != nil {
		return nil, err
	}
	resp.Providers, err = usageComposition(ctx, false)
	return resp, err
}

func usageDays(ctx context.Context, start, end time.Time) ([]dtosystem.TokenUsageDay, error) {
	rows, err := db.EntClient.QueryContext(ctx, usageLedger+`SELECT DATE(epoch,'unixepoch') AS date,
 COALESCE(SUM(CASE WHEN known=1 THEN total_tokens ELSE 0 END),0),COUNT(DISTINCT conversation_id)
 FROM usage WHERE epoch>=? AND epoch<? GROUP BY date ORDER BY date`, start.Unix(), end.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	days := make([]dtosystem.TokenUsageDay, 0)
	for rows.Next() {
		var day dtosystem.TokenUsageDay
		if err := rows.Scan(&day.Date, &day.TotalTokens, &day.Conversations); err != nil {
			return nil, err
		}
		days = append(days, day)
	}
	return days, rows.Err()
}

func usageComposition(ctx context.Context, model bool) ([]dtosystem.TokenUsageComposition, error) {
	id, name, group := "provider_id", "provider_name", "provider_id"
	if model {
		// 模型按实际 API ID 跨提供商合并，先汇总再取前 N 项；名称不参与分组。
		id, name, group = "model_id", "model_name", "model_id"
	}
	rows, err := db.EntClient.QueryContext(ctx, usageLedger+`SELECT `+id+`,MAX(`+name+`),
 CASE WHEN COUNT(DISTINCT provider_id)=1 THEN MAX(provider_id) ELSE '' END,
 CASE WHEN COUNT(DISTINCT provider_id)=1 THEN MAX(provider_name) ELSE '' END,
 SUM(input_tokens),SUM(output_tokens),SUM(reasoning_tokens),SUM(cached_tokens),SUM(total_tokens)
 FROM usage WHERE known=1 GROUP BY `+group+` ORDER BY SUM(total_tokens) DESC,`+id+` LIMIT ?`, usageCompositionLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]dtosystem.TokenUsageComposition, 0)
	for rows.Next() {
		var row dtosystem.TokenUsageComposition
		if err := rows.Scan(&row.ID, &row.Name, &row.ProviderID, &row.ProviderName, &row.InputTokens, &row.OutputTokens, &row.ReasoningTokens, &row.CachedTokens, &row.TotalTokens); err != nil {
			return nil, err
		}
		// 思考包含在输出内；缓存读取独立于输入。四个展示分量不重复相加。
		row.CachedTokens = min(row.CachedTokens, row.TotalTokens)
		output := min(row.OutputTokens, row.TotalTokens-row.CachedTokens)
		row.ReasoningTokens = min(row.ReasoningTokens, output)
		row.OutputTokens = output - row.ReasoningTokens
		row.InputTokens = row.TotalTokens - row.CachedTokens - output
		result = append(result, row)
	}
	return result, rows.Err()
}
