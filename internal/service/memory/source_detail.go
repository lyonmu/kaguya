package memory

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatblock"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorypage"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
)

// ErrSourceNotFound 表示来源不存在或已被物理清除。
var ErrSourceNotFound = errors.New("memory source not found")

// sourcePartLimit 与 sourcePartRunes 限制来源导航的单次展示量；正文只做截断
// 与脱敏，不把完整资料直接返回。
const (
	sourcePartLimit = 20
	sourcePartRunes = 500
)

// ListSources 管理界面查询来源状态，用于导入/回填进度与错误追踪。
func (s *Service) ListSources(ctx context.Context, req *dtomemory.MemorySourceListReq) (*dtomemory.MemorySourceListResp, error) {
	query := s.client.KaguyaMemorySource.Query()
	if req.ScopeKey != "" {
		if err := validateScopes([]string{req.ScopeKey}); err != nil {
			return nil, err
		}
		query.Where(kaguyamemorysource.ScopeKeyEQ(req.ScopeKey))
	}
	if req.Kind != "" {
		query.Where(kaguyamemorysource.KindEQ(kaguyamemorysource.Kind(req.Kind)))
	}
	if req.State != "" {
		query.Where(kaguyamemorysource.StateEQ(kaguyamemorysource.State(req.State)))
	}
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := query.Order(ent.Desc(kaguyamemorysource.FieldCapturedAt), ent.Desc(kaguyamemorysource.FieldID)).
		Offset((req.Page - 1) * req.PageSize).Limit(req.PageSize).All(ctx)
	if err != nil {
		return nil, err
	}
	resp := &dtomemory.MemorySourceListResp{
		Total: total, Page: req.Page, PageSize: req.PageSize,
		Items: make([]dtomemory.MemorySourceItemResp, 0, len(rows)),
	}
	for _, row := range rows {
		resp.Items = append(resp.Items, sourceItem(row))
	}
	return resp, nil
}

// SourceDetail 从 Memory 证据定位到原始来源：轮次/笔记/导入资料的可读定位
// 与有界片段。已删除或失效来源明确返回不可用，而不是静默为空。
func (s *Service) SourceDetail(ctx context.Context, id string) (*dtomemory.MemorySourceDetailResp, error) {
	src, err := s.client.KaguyaMemorySource.Query().Where(kaguyamemorysource.IDEQ(id)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrSourceNotFound
	}
	if err != nil {
		return nil, err
	}
	resp := &dtomemory.MemorySourceDetailResp{
		MemorySourceItemResp: sourceItem(src),
		Available:            true,
		Parts:                []dtomemory.MemorySourcePartResp{},
	}
	if src.State == kaguyamemorysource.StateExcluded {
		resp.Available, resp.UnavailableReason = false, "来源已失效（会话删除或隐私关闭）"
		return resp, nil
	}
	switch src.Kind {
	case kaguyamemorysource.KindTurn:
		turn, err := s.client.KaguyaChatTurn.Query().Where(kaguyachatturn.IDEQ(src.TurnID)).
			WithBlocks(func(q *ent.KaguyaChatBlockQuery) { q.Order(kaguyachatblock.BySequence()) }).Only(ctx)
		if ent.IsNotFound(err) {
			resp.Available, resp.UnavailableReason = false, "原始轮次已删除"
			return resp, nil
		}
		if err != nil {
			return nil, err
		}
		resp.TurnStatus, resp.FinishReason = string(turn.Status), turn.FinishReason
		resp.Parts = sourceParts(BuildTurnSegments(turn.UserContent, turn.Edges.Blocks))
	case kaguyamemorysource.KindNote:
		if src.RawContent != "" {
			resp.Parts = sourceParts(BuildNoteSegments(src.RawContent))
			break
		}
		pageID := strings.TrimPrefix(src.SourceKey, "note:")
		page, err := s.client.KaguyaMemoryPage.Query().
			Where(kaguyamemorypage.IDEQ(pageID), kaguyamemorypage.DeletedAtIsNil()).Only(ctx)
		if ent.IsNotFound(err) {
			resp.Available, resp.UnavailableReason = false, "笔记页面已删除"
			return resp, nil
		}
		if err != nil {
			return nil, err
		}
		resp.Parts = sourceParts(BuildNoteSegments(page.Body))
	case kaguyamemorysource.KindImport:
		if strings.TrimSpace(src.RawContent) == "" {
			resp.Available, resp.UnavailableReason = false, "导入快照已清除"
			return resp, nil
		}
		resp.Parts = sourceParts(BuildDocumentSegments(src.RawContent))
	default:
		resp.Available, resp.UnavailableReason = false, "未知来源类型"
	}
	return resp, nil
}

// sourceItem 生成来源列表/详情共用的摘要；不返回正文。
func sourceItem(src *ent.KaguyaMemorySource) dtomemory.MemorySourceItemResp {
	return dtomemory.MemorySourceItemResp{
		ID: src.ID, Kind: string(src.Kind), ScopeKey: src.ScopeKey, State: string(src.State),
		SourceKey: src.SourceKey, ConversationID: src.ConversationID, TurnID: src.TurnID,
		DocumentPath: src.DocumentPath, ContentHash: src.ContentHash, JobID: src.JobID,
		PolicyEpoch: src.PolicyEpoch, CapturedAt: src.CapturedAt,
	}
}

// sourceParts 返回脱敏、截断且有界的来源片段。
func sourceParts(segments []Segment) []dtomemory.MemorySourcePartResp {
	parts := make([]dtomemory.MemorySourcePartResp, 0, min(len(segments), sourcePartLimit))
	for i, segment := range segments {
		if i >= sourcePartLimit {
			break
		}
		text := redactSecrets(segment.Text)
		truncated := utf8.RuneCountInString(text) > sourcePartRunes
		parts = append(parts, dtomemory.MemorySourcePartResp{
			PartKey: segment.PartKey, Origin: segment.Origin,
			Text: truncateRunes(text, sourcePartRunes), Truncated: truncated,
		})
	}
	return parts
}
