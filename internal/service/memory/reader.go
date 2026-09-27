package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lyonmu/kaguya/internal/consts"
	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryjob"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorypage"
	"github.com/lyonmu/kaguya/internal/ent/kaguyasysteminfo"
)

// ScopedReader 是绑定作用域的只读记忆读取器，实现 memorytools.Reader 窄接口；
// 范围由服务端闭包绑定，模型只能传 query / page ID。
type ScopedReader struct {
	svc            *Service
	scopes         []string
	conversationID string
	policyEpoch    int64
}

// NewScopedReader 构造范围绑定的只读记忆工具接口。
func NewScopedReader(svc *Service, scopes []string) *ScopedReader {
	return &ScopedReader{svc: svc, scopes: scopes}
}

// NewConversationReader 除范围外还绑定本轮策略版本；撤销后旧工具立即拒绝读取。
func NewConversationReader(svc *Service, scopes []string, conversationID string, epoch int64) *ScopedReader {
	return &ScopedReader{svc: svc, scopes: append([]string(nil), scopes...), conversationID: conversationID, policyEpoch: epoch}
}

func (r *ScopedReader) checkPolicy(ctx context.Context) error {
	if r.conversationID == "" {
		return nil
	}
	return r.svc.CheckConversationEpoch(ctx, r.conversationID, r.policyEpoch)
}

// CheckConversationEpoch 是在途读取的撤销栅栏，epoch 不能替代当前授权检查。
func (s *Service) CheckConversationEpoch(ctx context.Context, conversationID string, epoch int64) error {
	policy, err := LoadPolicy(ctx, s.client)
	if err != nil {
		return err
	}
	if !policy.Enabled || policy.Epoch != epoch {
		return errors.New("memory policy changed")
	}
	conv, err := ResolveConversationPolicy(ctx, s.client, conversationID, policy)
	if err != nil {
		return err
	}
	if !conv.Recall {
		return errors.New("conversation memory is disabled")
	}
	return nil
}

// SearchMemory 返回 page_id、version、title、summary、status、source_count；
// 显式搜索可返回过期页面并标记，不返回删除或无权访问的页面。
func (r *ScopedReader) SearchMemory(ctx context.Context, query string, limit int) (string, error) {
	if err := r.checkPolicy(ctx); err != nil {
		return "", err
	}
	var pages []RetrievedPage
	var err error
	if strings.TrimSpace(query) == "" {
		pages, err = r.svc.CatalogPages(ctx, r.scopes, limit)
	} else {
		pages, err = r.svc.SearchPages(ctx, r.scopes, query, limit, true)
	}
	if err != nil {
		return "", err
	}
	if err := r.svc.prepareBlocks(ctx, pages); err != nil {
		return "", err
	}
	type item struct {
		PageID      string `json:"page_id"`
		Version     int64  `json:"version"`
		Title       string `json:"title"`
		Summary     string `json:"summary"`
		Status      string `json:"status"`
		SourceCount int    `json:"source_count"`
	}
	items := make([]item, 0, len(pages))
	for _, page := range pages {
		status := page.Status
		if page.Expired {
			status = "expired"
		}
		items = append(items, item{
			PageID: page.ID, Version: page.Version, Title: page.Title,
			Summary: page.Summary, Status: status, SourceCount: page.SourceCount,
		})
	}
	if len(items) == 0 {
		return `{"pages":[]}`, nil
	}
	data, err := json.Marshal(map[string]any{"pages": items})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ReadMemory 返回有界正文、主张证据摘要与关联页面 ID；版本参数用于核验时效。
func (r *ScopedReader) ReadMemory(ctx context.Context, pageID string, version int64, window ...int) (string, error) {
	if err := r.checkPolicy(ctx); err != nil {
		return "", err
	}
	detail, err := r.svc.ReadPageDetail(ctx, r.scopes, pageID, version)
	if err != nil {
		return "", err
	}
	offset, limit := 0, 8000
	if len(window) > 0 {
		offset = window[0]
	}
	if len(window) > 1 {
		limit = window[1]
	}
	body := []rune(detail.Body)
	if offset < 0 || offset > len(body) || limit < 1 || limit > 16000 {
		return "", errors.New("invalid memory body window")
	}
	end := min(offset+limit, len(body))
	var next *int
	if end < len(body) {
		next = &end
	}
	type readItem struct {
		Offset     int           `json:"offset"`
		NextOffset *int          `json:"next_offset"`
		TotalChars int           `json:"total_chars"`
		ID         string        `json:"id"`
		Version    int64         `json:"version"`
		Kind       string        `json:"kind"`
		Title      string        `json:"title"`
		Summary    string        `json:"summary"`
		Body       string        `json:"body"`
		Status     string        `json:"status"`
		Expired    bool          `json:"expired"`
		Claims     []ClaimDetail `json:"claims"`
		RelatedIDs []string      `json:"related_ids"`
	}
	expired := detail.ExpiresAt != nil && detail.ExpiresAt.Before(nowTime())
	data, err := json.Marshal(readItem{
		ID: detail.ID, Version: detail.Version, Kind: detail.Kind,
		Title: detail.Title, Summary: detail.Summary, Body: string(body[offset:end]),
		Status: detail.Status, Expired: expired,
		Offset: offset, NextOffset: next, TotalChars: len(body),
		Claims: detail.Claims, RelatedIDs: detail.RelatedIDs,
	})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Status 返回记忆开关、待处理规模、索引版本与独立的后台任务用量标签；
// 编译的任务模型消费不计入聊天轮次和聊天上下文占用。
func (s *Service) Status(ctx context.Context) (*dtomemory.MemoryStatusResp, error) {
	row, err := s.client.KaguyaSystemInfo.Query().Where(kaguyasysteminfo.IDEQ(consts.SystemInfoID)).
		Select(kaguyasysteminfo.FieldMemoryEnabled, kaguyasysteminfo.FieldMemoryAutoCapture,
			kaguyasysteminfo.FieldMemoryContextTokens, kaguyasysteminfo.FieldMemoryPolicyEpoch,
			kaguyasysteminfo.FieldTaskModelID).Only(ctx)
	if err != nil {
		return nil, err
	}
	resp := &dtomemory.MemoryStatusResp{
		Enabled: row.MemoryEnabled, AutoCapture: row.MemoryAutoCapture,
		ContextTokens: row.MemoryContextTokens, PolicyEpoch: row.MemoryPolicyEpoch,
		TaskModelSet:    row.TaskModelID != "",
		IndexNormalizer: NormalizerVersion,
	}
	if resp.PendingSources, err = pendingSourceCount(ctx, s.client); err != nil {
		return nil, err
	}
	if resp.ActivePages, err = s.client.KaguyaMemoryPage.Query().
		Where(kaguyamemorypage.StatusEQ(kaguyamemorypage.StatusActive), kaguyamemorypage.DeletedAtIsNil()).Count(ctx); err != nil {
		return nil, err
	}
	for _, status := range []struct {
		value kaguyamemoryjob.Status
		set   *int
	}{
		{kaguyamemoryjob.StatusBlocked, &resp.BlockedJobs},
		{kaguyamemoryjob.StatusFailed, &resp.FailedJobs},
		{kaguyamemoryjob.StatusNeedsReview, &resp.ReviewJobs},
	} {
		count, err := s.client.KaguyaMemoryJob.Query().
			Where(kaguyamemoryjob.StatusEQ(status.value)).Count(ctx)
		if err != nil {
			return nil, err
		}
		*status.set = count
	}
	attempts, err := s.client.KaguyaMemoryAttempt.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	resp.MemoryUsageKnown = true
	for _, attempt := range attempts {
		resp.MemoryCalls++
		if !attempt.UsageKnown {
			// 未知用量不是 0：单独标记，不能编造金额或确认消耗。
			resp.MemoryUsageKnown = false
			continue
		}
		resp.MemoryInputTokens += attempt.InputTokens
		resp.MemoryOutputTokens += attempt.OutputTokens
		resp.MemoryTotalTokens += attempt.TotalTokens
	}
	return resp, nil
}

// TurnMemoryRefs 解析轮次召回引用供界面展示；已删除页面显示“已删除”，
// 不能通过历史版本接口绕过删除。
func (s *Service) TurnMemoryRefs(ctx context.Context, selection dtomemory.TurnMemorySelection) (*dtomemory.MemoryRefsResp, error) {
	resp := &dtomemory.MemoryRefsResp{
		RetrieverVersion: selection.RetrieverVersion,
		EstimatedTokens:  selection.EstimatedTokens,
		Items:            make([]dtomemory.MemoryRefItemResp, 0, len(selection.Refs)),
	}
	for _, ref := range selection.Refs {
		item := dtomemory.MemoryRefItemResp{PageID: ref.PageID, Version: ref.Version}
		page, err := s.client.KaguyaMemoryPage.Get(ctx, ref.PageID)
		if ent.IsNotFound(err) || (err == nil && (page.DeletedAt != nil || page.Status == kaguyamemorypage.StatusDeleted)) {
			item.Title, item.Status, item.Deleted = "已删除", "deleted", true
			resp.Items = append(resp.Items, item)
			continue
		}
		if err != nil {
			return nil, err
		}
		item.Title, item.Status = page.Title, string(page.Status)
		resp.Items = append(resp.Items, item)
	}
	return resp, nil
}
