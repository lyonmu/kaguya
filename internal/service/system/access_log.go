package system

import (
	"context"
	"fmt"

	"entgo.io/ent/dialect/sql"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaaccesslog"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/quebec/pkg/code"
)

// CreateAccessLog 创建访问日志
func (s *SystemSvc) CreateAccessLog(ctx context.Context, req *dtosystem.SystemAccessLogReq) error {

	// 创建访问日志
	if _, err := db.EntClient.KaguyaAccessLog.Create().
		SetAccessIP(req.AccessIP).
		SetAccessTime(req.AccessTime).
		SetOs(req.Os).
		SetPlatform(req.Platform).
		SetBrowserName(req.BrowserName).
		SetBrowserVersion(req.BrowserVersion).
		SetBrowserEngineName(req.BrowserEngineName).
		SetBrowserEngineVersion(req.BrowserEngineVersion).
		Save(ctx); err != nil {
		global.Logger.Sugar().Errorf("Failed to create operation log: %v", err)
		return fmt.Errorf("failed to create operation log: %w", err)
	}

	return nil
}

// AccessLogPage 分页查询访问日志
func (s *SystemSvc) AccessLogPage(ctx context.Context, req *dtosystem.SystemAccessLogPageReq) (*dtosystem.SystemAccessLogListResp, error) {
	var (
		total    int
		items    = make([]*dtosystem.SystemAccessLogResp, 0)
		page     = (req.Page - 1) * req.PageSize
		pageSize = req.PageSize
		resp     = &dtosystem.SystemAccessLogListResp{}
		query    = db.EntClient.KaguyaAccessLog.Query()
	)

	if len(req.AccessIP) > 0 {
		query = query.Where(kaguyaaccesslog.AccessIPContains(req.AccessIP))
	}

	if req.StartTime > 0 {
		query = query.Where(kaguyaaccesslog.AccessTimeGTE(req.StartTime))
	}

	if req.EndTime > 0 {
		query = query.Where(kaguyaaccesslog.AccessTimeLTE(req.EndTime))
	}

	total, err := query.Count(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("select access log failed: %s", err)
		return nil, &code.LogQueryFailed
	}

	rows, err := query.Offset(page).Limit(pageSize).Order(kaguyaaccesslog.ByAccessTime(sql.OrderDesc())).All(ctx)
	if err != nil {
		global.Logger.Sugar().Errorf("select access log failed: %s", err)
		return nil, &code.LogQueryFailed
	}

	for _, row := range rows {
		item := dtosystem.SystemAccessLogResp{}
		item.LoadDb(row)
		items = append(items, &item)
	}

	resp.Total = total
	resp.Items = items
	resp.Page = req.Page
	resp.PageSize = pageSize

	return resp, nil
}
