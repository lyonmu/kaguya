package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lyonmu/kaguya/internal/db"
	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorypage"
)

// Export 是用户确认的 Markdown 导出：正文可读、可携带，
// 但不作为主存储，也不写入临时目录明文副本。
func (s *Service) Export(ctx context.Context, req *dtomemory.MemoryExportReq) (*dtomemory.MemoryExportResp, error) {
	query := s.client.KaguyaMemoryPage.Query()
	switch {
	case len(req.PageIDs) > 0:
		query.Where(kaguyamemorypage.IDIn(req.PageIDs...))
	case len(req.ScopeKeys) > 0:
		if err := validateScopes(req.ScopeKeys); err != nil {
			return nil, err
		}
		query.Where(kaguyamemorypage.ScopeKeyIn(req.ScopeKeys...))
	default:
		return nil, fmt.Errorf("%w: export requires scope_keys or page_ids", ErrPlanInvalid)
	}
	if !req.IncludeDeleted {
		query.Where(kaguyamemorypage.StatusNEQ(kaguyamemorypage.StatusDeleted))
	}
	pages, err := query.Order(kaguyamemorypage.ByScopeKey(), kaguyamemorypage.ByCanonicalKey()).All(ctx)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Kaguya Memory 导出\n\n导出时间：%s\n页面数量：%d\n\n---\n", nowTime().UTC().Format(time.RFC3339), len(pages))
	for _, page := range pages {
		b.WriteString("\n## ")
		b.WriteString(strings.ReplaceAll(page.Title, "\n", " "))
		b.WriteString("\n\n")
		fmt.Fprintf(&b, "- 作用域：%s\n- 类型：%s\n- 状态：%s\n- 版本：%d\n- 主题键：%s\n",
			page.ScopeKey, page.Kind, page.Status, page.Version, page.CanonicalKey)
		if page.ExpiresAt != nil {
			fmt.Fprintf(&b, "- 过期时间：%s\n", page.ExpiresAt.UTC().Format(time.RFC3339))
		}
		if len(page.Aliases) > 0 {
			aliases := append([]string{}, page.Aliases...)
			sort.Strings(aliases)
			fmt.Fprintf(&b, "- 别名：%s\n", strings.Join(aliases, "、"))
		}
		detail, err := s.ReadPageDetail(ctx, []string{page.ScopeKey}, page.ID, 0)
		if err != nil {
			return nil, err
		}
		if len(detail.Claims) > 0 {
			b.WriteString("\n### 依据\n")
			for _, claim := range detail.Claims {
				fmt.Fprintf(&b, "- [%s] %s\n", claim.Basis, sanitizeInline(claim.Statement))
				for _, ev := range claim.Evidence {
					fmt.Fprintf(&b, "  - %s（%s/%s）：%s\n", ev.Relation, ev.Source, ev.PartKey, sanitizeInline(ev.Quote))
				}
			}
		}
		b.WriteString("\n### 正文\n\n")
		b.WriteString(page.Body)
		b.WriteString("\n")
	}
	return &dtomemory.MemoryExportResp{
		Filename: fmt.Sprintf("kaguya-memory-%s.md", nowTime().UTC().Format("20060102-150405")),
		Markdown: b.String(),
	}, nil
}

// EnsureSearchProjection 启动时同步投影迁移：normalizer 版本变化后从页面重新生成
// SearchDoc 并重建索引。单执行 FTS rebuild 不能升级投影文本，必须重新生成投影。
// 小库启动时同步执行；调用方先确保 FTS 设施已迁移。
func EnsureSearchProjection(ctx context.Context, client *ent.Client) error {
	value, ok, err := db.MemoryIndexMeta(ctx, client, db.MemoryIndexMetaNormalizer)
	if err != nil {
		return err
	}
	if ok && value == fmt.Sprintf("%d", NormalizerVersion) {
		return nil
	}
	tx, err := client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txC := tx.Client()
	if _, err := txC.KaguyaMemorySearchDoc.Delete().Exec(ctx); err != nil {
		return err
	}
	pages, err := txC.KaguyaMemoryPage.Query().
		Where(kaguyamemorypage.StatusEQ(kaguyamemorypage.StatusActive), kaguyamemorypage.DeletedAtIsNil()).
		All(ctx)
	if err != nil {
		return err
	}
	for _, page := range pages {
		if err := writeSearchDocTx(ctx, txC, page); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := db.RebuildMemoryFTS(ctx, client); err != nil {
		return err
	}
	return db.SetMemoryIndexMeta(ctx, client, db.MemoryIndexMetaNormalizer, fmt.Sprintf("%d", NormalizerVersion))
}
