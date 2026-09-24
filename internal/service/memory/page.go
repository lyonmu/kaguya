package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryevidence"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorypage"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryrevision"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
)

// ErrPageVersionConflict 表示人工并发编辑冲突；返回明确冲突，
// 而非最后写入者悄悄覆盖。
var ErrPageVersionConflict = errors.New("memory page version conflict")

// manualClaimKey 是人工保存页面的自证主张键。
const manualClaimKey = "note"

// CreatePage 同步保存用户手写记忆为人工修订，无需任务模型。
// 可选来源引用定位用户选中的真实轮次文字；选择助手文字只代表用户认可保存，
// 不伪装成工具核实。
func (s *Service) CreatePage(ctx context.Context, req *dtomemory.MemoryPageSaveReq) (*MemoryPageDetail, error) {
	canonicalKey := req.CanonicalKey
	if strings.TrimSpace(canonicalKey) == "" {
		canonicalKey = deriveCanonicalKey(req.Title)
	}
	if err := ValidateManualPage(req.Title, req.Summary, req.Body, canonicalKey, req.Aliases); err != nil {
		return nil, err
	}
	if err := ValidateScope(ctx, s.client, req.ScopeKey); err != nil {
		return nil, err
	}
	policy, err := LoadPolicy(ctx, s.client)
	if err != nil {
		return nil, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()

	var sourceRef *dtomemory.MemorySourceRefReq
	var turnSource *ent.KaguyaMemorySource
	if req.Source != nil {
		source, err := EnsureTurnSourceTx(ctx, client, req.Source.ConversationID, req.Source.TurnID)
		if err != nil {
			return nil, err
		}
		if source.ScopeKey != req.ScopeKey {
			// 来源与页面必须同作用域，防止把项目文字泄入个人记忆。
			return nil, fmt.Errorf("%w: source scope %q does not match page scope", ErrPlanInvalid, source.ScopeKey)
		}
		if err := validateSourceQuote(ctx, client, source, req.Source.PartKey, req.Source.Quote); err != nil {
			return nil, err
		}
		sourceRef, turnSource = req.Source, source
	}
	create := client.KaguyaMemoryPage.Create().
		SetScopeKey(req.ScopeKey).SetCanonicalKey(canonicalKey).
		SetKind(kaguyamemorypage.Kind(req.Kind)).
		SetTitle(req.Title).SetSummary(req.Summary).SetBody(req.Body).
		SetAliases(defaultAliases(req.Aliases)).
		SetStatus(kaguyamemorypage.StatusActive).SetVersion(1)
	if req.ExpiresAt != nil {
		create.SetExpiresAt(*req.ExpiresAt)
	}
	if req.Pinned != nil {
		create.SetPinned(*req.Pinned)
	}
	if req.UserLocked != nil {
		create.SetUserLocked(*req.UserLocked)
	}
	row, err := create.Save(ctx)
	if ent.IsConstraintError(err) {
		// tombstone 占位阻止自动复活；用户显式重建同主题时复活为新修订。
		existing, queryErr := client.KaguyaMemoryPage.Query().
			Where(kaguyamemorypage.ScopeKeyEQ(req.ScopeKey), kaguyamemorypage.CanonicalKeyEQ(canonicalKey)).
			Only(ctx)
		if queryErr != nil {
			return nil, queryErr
		}
		if existing.Status != kaguyamemorypage.StatusDeleted {
			return nil, ErrPageVersionConflict
		}
		row, err = revivePageTx(ctx, client, existing, req)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	page := row

	note, err := EnsureNoteSourceTx(ctx, client, page, policy.Epoch)
	if err != nil {
		return nil, err
	}
	change := manualPatch(req, canonicalKey)
	change.Claims = manualClaims(page, note, turnSource, sourceRef)
	if err := saveRevisionTx(ctx, client, page, change, actorUser, ""); err != nil {
		return nil, err
	}
	if err := replaceRelatedLinksTx(ctx, client, page.ID, req.RelatedIDs); err != nil {
		return nil, err
	}
	if err := writeSearchDocTx(ctx, client, page); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	Notify()
	return s.ReadPageDetail(ctx, []string{req.ScopeKey}, page.ID, 0)
}

// revivePageTx 用户显式重建 tombstone 主题：复活为新修订，不回退版本号。
func revivePageTx(ctx context.Context, client *ent.Client, existing *ent.KaguyaMemoryPage, req *dtomemory.MemoryPageSaveReq) (*ent.KaguyaMemoryPage, error) {
	update := client.KaguyaMemoryPage.UpdateOneID(existing.ID).
		SetKind(kaguyamemorypage.Kind(req.Kind)).
		SetTitle(req.Title).SetSummary(req.Summary).SetBody(req.Body).
		SetAliases(defaultAliases(req.Aliases)).
		SetStatus(kaguyamemorypage.StatusActive).SetVersion(existing.Version + 1)
	if req.ExpiresAt != nil {
		update.SetExpiresAt(*req.ExpiresAt)
	}
	return update.Save(ctx)
}

// UpdatePage 带 expected_version 的人工编辑/置顶/锁定；
// 每次编辑保存人工修订快照，可显式提升到 shared。
func (s *Service) UpdatePage(ctx context.Context, id string, req *dtomemory.MemoryPageUpdateReq) (*MemoryPageDetail, error) {
	policy, err := LoadPolicy(ctx, s.client)
	if err != nil {
		return nil, err
	}
	if req.ScopeKey != "" && req.ScopeKey != ScopeShared {
		// 只支持显式提升到 shared；项目历史不会借此改写归属。
		return nil, fmt.Errorf("%w: scope can only be promoted to shared", ErrPlanInvalid)
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	page, err := client.KaguyaMemoryPage.Query().
		Where(kaguyamemorypage.IDEQ(id), kaguyamemorypage.DeletedAtIsNil()).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrPageNotFound
	}
	if err != nil {
		return nil, err
	}
	if page.Status == kaguyamemorypage.StatusDeleted {
		return nil, ErrPageNotFound
	}
	if page.Version != req.ExpectedVersion {
		return nil, ErrPageVersionConflict
	}
	if req.Kind != "" {
		page.Kind = kaguyamemorypage.Kind(req.Kind)
	}
	if strings.TrimSpace(req.CanonicalKey) != "" {
		page.CanonicalKey = req.CanonicalKey
	}
	if strings.TrimSpace(req.Title) != "" {
		page.Title = req.Title
	}
	if req.Summary != nil {
		page.Summary = *req.Summary
	}
	if req.Body != nil {
		page.Body = *req.Body
	}
	if req.Aliases != nil {
		page.Aliases = req.Aliases
	}
	if req.ClearExpiresAt {
		page.ExpiresAt = nil
	} else if req.ExpiresAt != nil {
		page.ExpiresAt = req.ExpiresAt
	}
	if req.Pinned != nil {
		page.Pinned = *req.Pinned
	}
	if req.UserLocked != nil {
		page.UserLocked = *req.UserLocked
	}
	if req.ScopeKey != "" {
		page.ScopeKey = req.ScopeKey
	}
	canonical := page.CanonicalKey
	if err := ValidateManualPage(page.Title, page.Summary, page.Body, canonical, page.Aliases); err != nil {
		return nil, err
	}
	var sourceRef *dtomemory.MemorySourceRefReq
	var turnSource *ent.KaguyaMemorySource
	if req.Source != nil {
		source, err := EnsureTurnSourceTx(ctx, client, req.Source.ConversationID, req.Source.TurnID)
		if err != nil {
			return nil, err
		}
		if source.ScopeKey != page.ScopeKey {
			return nil, fmt.Errorf("%w: source scope %q does not match page scope", ErrPlanInvalid, source.ScopeKey)
		}
		if err := validateSourceQuote(ctx, client, source, req.Source.PartKey, req.Source.Quote); err != nil {
			return nil, err
		}
		sourceRef, turnSource = req.Source, source
	}

	// 条件更新防止并发覆盖；用户锁定不影响人工编辑，只阻止自动覆盖。
	update := client.KaguyaMemoryPage.UpdateOneID(page.ID).
		SetKind(page.Kind).SetCanonicalKey(page.CanonicalKey).
		SetTitle(page.Title).SetSummary(page.Summary).SetBody(page.Body).
		SetAliases(defaultAliases(page.Aliases)).
		SetPinned(page.Pinned).SetUserLocked(page.UserLocked).
		SetVersion(page.Version + 1).
		Where(kaguyamemorypage.VersionEQ(req.ExpectedVersion))
	if req.ClearExpiresAt {
		update.ClearExpiresAt()
	}
	updated, err := update.Save(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrPageVersionConflict
	}
	if err != nil {
		return nil, err
	}
	note, err := EnsureNoteSourceTx(ctx, client, updated, policy.Epoch)
	if err != nil {
		return nil, err
	}
	change := &dtomemory.PagePatch{
		Kind: string(updated.Kind), CanonicalKey: updated.CanonicalKey,
		Title: updated.Title, Summary: updated.Summary, Body: updated.Body,
		Aliases: updated.Aliases, Reason: req.Reason,
		Claims: manualClaims(updated, note, turnSource, sourceRef),
	}
	if err := saveRevisionTx(ctx, client, updated, change, actorUser, ""); err != nil {
		return nil, err
	}
	if req.RelatedIDs != nil {
		if err := replaceRelatedLinksTx(ctx, client, updated.ID, req.RelatedIDs); err != nil {
			return nil, err
		}
	}
	if err := writeSearchDocTx(ctx, client, updated); err != nil {
		return nil, err
	}
	if req.ScopeKey != "" {
		if err := client.KaguyaMemorySource.Update().
			Where(kaguyamemorysource.SourceKeyEQ("note:" + updated.ID)).
			SetScopeKey(updated.ScopeKey).Exec(ctx); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	scopes := []string{updated.ScopeKey}
	return s.ReadPageDetail(ctx, scopes, updated.ID, 0)
}

// DeletePage 是明确的删除语义：disable 停用（可恢复、内容保留）；
// forget 删除记忆（撤出索引、清除正文/修订/证据摘录，保留最小抑制标记）。
// 删除立即同步生效，不排队等模型决定，并提升策略版本防止在途任务复活。
func (s *Service) DeletePage(ctx context.Context, id, mode string) error {
	if mode == "" {
		mode = "disable"
	}
	if mode != "disable" && mode != "forget" {
		return fmt.Errorf("%w: unknown delete mode %q", ErrPlanInvalid, mode)
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	page, err := client.KaguyaMemoryPage.Query().
		Where(kaguyamemorypage.IDEQ(id), kaguyamemorypage.DeletedAtIsNil()).Only(ctx)
	if ent.IsNotFound(err) {
		return ErrPageNotFound
	}
	if err != nil {
		return err
	}
	if page.Status != kaguyamemorypage.StatusDeleted {
		if mode == "forget" {
			// 遗忘前记录独占支持来源，证据清除后据此设置排除标记，
			// 避免待处理作业从同一来源重建再提取。
			supports, err := pageSupportSourceIDs(ctx, client, page.ID)
			if err != nil {
				return err
			}
			if err := forgetPageTx(ctx, client, page, actorUser, "forgotten by user"); err != nil {
				return err
			}
			// 笔记来源随页面遗忘。
			if _, err := client.KaguyaMemorySource.Delete().
				Where(kaguyamemorysource.SourceKeyEQ("note:" + page.ID)).Exec(ctx); err != nil {
				return err
			}
			if err := excludePendingSupportTx(ctx, client, page.ID, supports); err != nil {
				return err
			}
		} else {
			if _, err := setPageStatusTx(ctx, client, page, kaguyamemorypage.StatusArchived, actorUser, "", "disabled by user"); err != nil {
				return err
			}
		}
	}
	if mode == "forget" {
		if err := BumpPolicyEpoch(ctx, client); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	Notify()
	return nil
}

// pageSupportSourceIDs 返回页面修订证据引用的来源 ID 集合。
func pageSupportSourceIDs(ctx context.Context, client *ent.Client, pageID string) ([]string, error) {
	revisions, err := client.KaguyaMemoryRevision.Query().
		Where(kaguyamemoryrevision.PageIDEQ(pageID)).
		IDs(ctx)
	if err != nil || len(revisions) == 0 {
		return nil, err
	}
	rows, err := client.KaguyaMemoryEvidence.Query().
		Where(kaguyamemoryevidence.RevisionIDIn(revisions...)).
		Select(kaguyamemoryevidence.FieldID, kaguyamemoryevidence.FieldSourceID).All(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.SourceID != "" && !seen[row.SourceID] {
			seen[row.SourceID] = true
			ids = append(ids, row.SourceID)
		}
	}
	return ids, nil
}

// excludePendingSupportTx 把仍待处理、且只支持被遗忘页面的来源标记排除，
// 避免重建再提取；还支持其他页面的来源不做全局排除。
// 注意：调用时页面修订的证据行已被 forgetPageTx 清除，这里按来源当前剩余证据
// 与删除前快照判断独占性。
func excludePendingSupportTx(ctx context.Context, client *ent.Client, pageID string, exclusiveSourceIDs []string) error {
	for _, sourceID := range exclusiveSourceIDs {
		remaining, err := client.KaguyaMemoryEvidence.Query().
			Where(kaguyamemoryevidence.SourceIDEQ(sourceID)).Exist(ctx)
		if err != nil {
			return err
		}
		if remaining {
			continue
		}
		if err := client.KaguyaMemorySource.Update().
			Where(kaguyamemorysource.IDEQ(sourceID),
				kaguyamemorysource.StateIn(kaguyamemorysource.StatePending, kaguyamemorysource.StateClaimed)).
			SetState(kaguyamemorysource.StateExcluded).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

// ListPages 管理界面查询：可显式查看所有范围（聊天自动召回不能如此）。
func (s *Service) ListPages(ctx context.Context, req *dtomemory.MemoryPageListReq) (*dtomemory.MemoryPageListResp, error) {
	query := s.client.KaguyaMemoryPage.Query().Where(kaguyamemorypage.DeletedAtIsNil())
	if req.ScopeKey != "" {
		// 管理查询允许历史项目作用域；只校验取值形状，不校验项目是否仍存在。
		if err := validateScopes([]string{req.ScopeKey}); err != nil {
			return nil, err
		}
		query.Where(kaguyamemorypage.ScopeKeyEQ(req.ScopeKey))
	}
	if req.Status != "" {
		query.Where(kaguyamemorypage.StatusEQ(kaguyamemorypage.Status(req.Status)))
	} else {
		// 默认不含 tombstone。
		query.Where(kaguyamemorypage.StatusNEQ(kaguyamemorypage.StatusDeleted))
	}
	if req.Keyword != "" {
		query.Where(kaguyamemorypage.Or(
			kaguyamemorypage.TitleContainsFold(req.Keyword),
			kaguyamemorypage.SummaryContainsFold(req.Keyword),
		))
	}
	if req.Pinned != nil {
		query.Where(kaguyamemorypage.PinnedEQ(*req.Pinned))
	}
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := query.Order(ent.Desc(kaguyamemorypage.FieldUpdatedAt)).
		Offset((req.Page - 1) * req.PageSize).Limit(req.PageSize).All(ctx)
	if err != nil {
		return nil, err
	}
	resp := &dtomemory.MemoryPageListResp{
		Total: total, Page: req.Page, PageSize: req.PageSize,
		Items: make([]dtomemory.MemoryPageResp, 0, len(rows)),
	}
	for _, row := range rows {
		resp.Items = append(resp.Items, dtomemory.MemoryPageResp{
			ID: row.ID, ScopeKey: row.ScopeKey, Kind: string(row.Kind), CanonicalKey: row.CanonicalKey,
			Title: row.Title, Summary: row.Summary, Status: string(row.Status), Version: row.Version,
			Pinned: row.Pinned, UserLocked: row.UserLocked, ExpiresAt: row.ExpiresAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return resp, nil
}

// ListRevisions 返回页面修订历史，受删除与范围权限约束。
func (s *Service) ListRevisions(ctx context.Context, scopes []string, id string) ([]dtomemory.MemoryRevisionResp, error) {
	detail, err := s.ReadPageDetail(ctx, scopes, id, 0)
	if err != nil {
		return nil, err
	}
	rows, err := s.client.KaguyaMemoryRevision.Query().
		Where(kaguyamemoryrevision.PageIDEQ(detail.ID)).
		Order(ent.Desc(kaguyamemoryrevision.FieldVersion)).All(ctx)
	if err != nil {
		return nil, err
	}
	resp := make([]dtomemory.MemoryRevisionResp, 0, len(rows))
	for _, row := range rows {
		keys := make([]string, 0, len(row.Claims))
		for _, claim := range row.Claims {
			keys = append(keys, claim.Key)
		}
		resp = append(resp, dtomemory.MemoryRevisionResp{
			Version: row.Version, Actor: string(row.Actor), JobID: row.JobID, Reason: row.Reason,
			Title: row.Title, Summary: row.Summary, Body: row.Body, Status: string(row.Status),
			ClaimKeys: keys, CreatedAt: row.CreatedAt,
		})
	}
	return resp, nil
}

// RestoreRevision 以旧内容新建恢复修订，不回退版本号；
// 被遗忘且已物理清除的修订不能恢复。
func (s *Service) RestoreRevision(ctx context.Context, scopes []string, id string, version int64) (*MemoryPageDetail, error) {
	if err := validateScopes(scopes); err != nil {
		return nil, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	page, err := client.KaguyaMemoryPage.Query().
		Where(kaguyamemorypage.IDEQ(id), kaguyamemorypage.DeletedAtIsNil()).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrPageNotFound
	}
	if err != nil {
		return nil, err
	}
	if page.Status == kaguyamemorypage.StatusDeleted {
		return nil, ErrPageNotFound
	}
	revision, err := client.KaguyaMemoryRevision.Query().
		Where(kaguyamemoryrevision.PageIDEQ(id), kaguyamemoryrevision.VersionEQ(version)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrPageVersionGone
	}
	if err != nil {
		return nil, err
	}
	updated, err := client.KaguyaMemoryPage.UpdateOneID(page.ID).
		SetKind(kaguyamemorypage.Kind(revision.Kind)).SetTitle(revision.Title).SetSummary(revision.Summary).
		SetBody(revision.Body).SetAliases(defaultAliases(revision.Aliases)).
		SetStatus(kaguyamemorypage.Status(revision.Status)).SetVersion(page.Version + 1).
		Where(kaguyamemorypage.VersionEQ(page.Version)).Save(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrPageVersionConflict
	}
	if err != nil {
		return nil, err
	}
	change := &dtomemory.PagePatch{
		Kind: string(revision.Kind), Title: revision.Title, Summary: revision.Summary,
		Body: revision.Body, Aliases: revision.Aliases,
		Reason: fmt.Sprintf("restore revision v%d", version),
	}
	// 保留旧修订的主张与证据引用，保持溯源连续。
	claims := make([]dtomemory.ClaimPatch, 0, len(revision.Claims))
	evidence := make(map[string][]dtomemory.ClaimEvidence)
	rows, err := client.KaguyaMemoryEvidence.Query().
		Where(kaguyamemoryevidence.RevisionIDEQ(revision.ID)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		evidence[row.ClaimKey] = append(evidence[row.ClaimKey], dtomemory.ClaimEvidence{
			SourceID: row.SourceID, PartKey: row.PartKey, Quote: row.Quote, Relation: string(row.Relation),
		})
	}
	for _, claim := range revision.Claims {
		claims = append(claims, dtomemory.ClaimPatch{
			Key: claim.Key, Statement: claim.Statement, Basis: claim.Basis, Evidence: evidence[claim.Key],
		})
	}
	change.Claims = claims
	if err := saveRevisionTx(ctx, client, updated, change, actorUser, ""); err != nil {
		return nil, err
	}
	if err := writeSearchDocTx(ctx, client, updated); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.ReadPageDetail(ctx, scopes, updated.ID, 0)
}

// manualPatch 生成人工修订的变更快照。
func manualPatch(req *dtomemory.MemoryPageSaveReq, canonicalKey string) *dtomemory.PagePatch {
	return &dtomemory.PagePatch{
		Kind: req.Kind, CanonicalKey: canonicalKey,
		Title: req.Title, Summary: req.Summary, Body: req.Body,
		Aliases: req.Aliases, Reason: req.Reason,
	}
}

// manualClaims 构造人工保存的自证主张：笔记正文即用户陈述；
// 有来源引用时追加真实轮次证据。选择助手文字只代表用户认可保存，不是工具核实。
func manualClaims(page *ent.KaguyaMemoryPage, note, turnSource *ent.KaguyaMemorySource, sourceRef *dtomemory.MemorySourceRefReq) []dtomemory.ClaimPatch {
	quote := truncateRunes(page.Body, 200)
	if strings.TrimSpace(quote) == "" {
		quote = page.Title
	}
	claim := dtomemory.ClaimPatch{
		Key: manualClaimKey, Statement: truncateRunes(page.Title+"："+page.Summary, 400),
		Basis: "user_statement",
		Evidence: []dtomemory.ClaimEvidence{
			{SourceID: note.ID, PartKey: "note", Quote: quote, Relation: "support"},
		},
	}
	if sourceRef != nil && turnSource != nil {
		claim.Evidence = append(claim.Evidence, dtomemory.ClaimEvidence{
			SourceID: turnSource.ID, PartKey: sourceRef.PartKey, Quote: sourceRef.Quote, Relation: "support",
		})
	}
	return []dtomemory.ClaimPatch{claim}
}

// validateSourceQuote 校验 quote 确实是来源片段子串；非子串 quote 直接拒绝。
func validateSourceQuote(ctx context.Context, client *ent.Client, source *ent.KaguyaMemorySource, partKey, quote string) error {
	segments, err := LoadSourceSegments(ctx, client, source)
	if err != nil {
		return err
	}
	for _, segment := range segments {
		if segment.PartKey == partKey {
			if quote == "" || !strings.Contains(segment.Text, quote) {
				return fmt.Errorf("%w: quote is not a substring of source part %q", ErrPlanInvalid, partKey)
			}
			return nil
		}
	}
	return fmt.Errorf("%w: unknown source part %q", ErrPlanInvalid, partKey)
}

// deriveCanonicalKey 从标题派生稳定主题键；重命名不改变已有键。
func deriveCanonicalKey(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', isCJK(r):
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	key := strings.Trim(b.String(), "-")
	if key == "" {
		key = "memory"
	}
	if utf8.RuneCountInString(key) > maxCanonicalRunes {
		key = string([]rune(key)[:maxCanonicalRunes])
	}
	return key
}
