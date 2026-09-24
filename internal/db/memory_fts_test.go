package db_test

import (
	"context"
	"testing"

	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/ent"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
)

func setupMemoryFTS(t *testing.T) (context.Context, *ent.Client, config.DatabaseConfig) {
	t.Helper()
	cfg := encryptedConfig(t)
	if err := cfg.EnsureSQLiteDatabase(); err != nil {
		t.Fatal(err)
	}
	client, err := db.InitSQLite(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return context.Background(), client, cfg
}

func insertSearchDoc(t *testing.T, ctx context.Context, client *ent.Client, page string, version int64, title string) *ent.KaguyaMemorySearchDoc {
	t.Helper()
	row, err := client.KaguyaMemorySearchDoc.Create().
		SetPageID(page).SetPageVersion(version).SetNormalizerVersion(1).
		SetTitleTerms(title).SetAliasTerms("").SetSummaryTerms("").SetBodyTerms("body").
		Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func ftsMatches(t *testing.T, ctx context.Context, client *ent.Client, match string) []int64 {
	t.Helper()
	rows, err := client.QueryContext(ctx,
		"SELECT rowid FROM kaguya_memory_fts WHERE kaguya_memory_fts MATCH ? ORDER BY rowid", match)
	if err != nil {
		t.Fatalf("FTS match %q: %v", match, err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// FTS 虚拟表、triggers 与投影在同一套初始化逻辑下工作：触发器负责投影与索引同步，
// 事务回滚不留下半索引。
func TestMemoryFTSIndexFollowsSearchDoc(t *testing.T) {
	ctx, client, _ := setupMemoryFTS(t)
	first := insertSearchDoc(t, ctx, client, "page-1", 1, "sqlcipher")
	insertSearchDoc(t, ctx, client, "page-2", 1, "postgres")
	if ids := ftsMatches(t, ctx, client, "sqlcipher"); len(ids) != 1 || ids[0] != first.ID {
		t.Fatalf("insert not indexed: %v", ids)
	}
	if _, err := client.KaguyaMemorySearchDoc.UpdateOneID(first.ID).SetTitleTerms("kaguya").Save(ctx); err != nil {
		t.Fatal(err)
	}
	if ids := ftsMatches(t, ctx, client, "sqlcipher"); len(ids) != 0 {
		t.Fatalf("stale term still matches after update: %v", ids)
	}
	if ids := ftsMatches(t, ctx, client, "kaguya"); len(ids) != 1 || ids[0] != first.ID {
		t.Fatalf("update not indexed: %v", ids)
	}
	if err := client.KaguyaMemorySearchDoc.DeleteOneID(first.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if ids := ftsMatches(t, ctx, client, "kaguya"); len(ids) != 0 {
		t.Fatalf("deleted projection still matches: %v", ids)
	}

	// 回滚的投影不能出现在索引里。
	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.KaguyaMemorySearchDoc.Create().
		SetPageID("page-3").SetPageVersion(1).SetNormalizerVersion(1).
		SetTitleTerms("rolledback").SetAliasTerms("").SetSummaryTerms("").SetBodyTerms("").
		Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if ids := ftsMatches(t, ctx, client, "rolledback"); len(ids) != 0 {
		t.Fatalf("rolled back projection indexed: %v", ids)
	}
}

// 显式 INTEGER PRIMARY KEY 经 VACUUM 后 rowid 映射保持稳定。
func TestMemoryFTSRowidSurvivesVacuum(t *testing.T) {
	ctx, client, _ := setupMemoryFTS(t)
	row := insertSearchDoc(t, ctx, client, "page-1", 1, "sqlcipher")
	if _, err := client.ExecContext(ctx, "VACUUM"); err != nil {
		t.Fatal(err)
	}
	if ids := ftsMatches(t, ctx, client, "sqlcipher"); len(ids) != 1 || ids[0] != row.ID {
		t.Fatalf("rowid mapping changed after VACUUM: %v", ids)
	}
}

// 索引结构版本变化时显式替换虚拟表与 triggers，并重建、校验已有投影；
// 重复初始化保持幂等。Ent 自动迁移不能误处理 FTS shadow tables。
func TestMemoryFTSSchemaUpgradeAndReopen(t *testing.T) {
	ctx, client, cfg := setupMemoryFTS(t)
	row := insertSearchDoc(t, ctx, client, "page-1", 1, "sqlcipher")
	if err := db.EnsureMemoryFTS(ctx, client); err != nil {
		t.Fatalf("idempotent migrate: %v", err)
	}
	// 模拟旧版本索引：清掉版本标记后必须重建并保持可用。
	if err := db.SetMemoryIndexMeta(ctx, client, db.MemoryIndexMetaFTSSchema, "0"); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureMemoryFTS(ctx, client); err != nil {
		t.Fatalf("upgrade migrate: %v", err)
	}
	if ids := ftsMatches(t, ctx, client, "sqlcipher"); len(ids) != 1 || ids[0] != row.ID {
		t.Fatalf("projection lost after rebuild: %v", ids)
	}
	version, ok, err := db.MemoryIndexMeta(ctx, client, db.MemoryIndexMetaFTSSchema)
	if err != nil || !ok || version != "1" {
		t.Fatalf("fts schema version=%q ok=%v err=%v", version, ok, err)
	}

	// 完整重开：Ent Schema.Create 在已存在 FTS shadow tables 的库上仍成功，
	// 且 FTS 迁移保持幂等。
	reopened, err := db.InitSQLite(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	count, err := reopened.KaguyaMemorySearchDoc.Query().Count(ctx)
	if err != nil || count != 1 {
		t.Fatalf("search docs after reopen: count=%d err=%v", count, err)
	}
}

// 规范化版本保存在索引元数据中，供投影升级流程对比。
func TestMemoryIndexMetaRoundTrip(t *testing.T) {
	ctx, client, _ := setupMemoryFTS(t)
	if _, ok, err := db.MemoryIndexMeta(ctx, client, db.MemoryIndexMetaNormalizer); err != nil || ok {
		t.Fatalf("missing meta: ok=%v err=%v", ok, err)
	}
	if err := db.SetMemoryIndexMeta(ctx, client, db.MemoryIndexMetaNormalizer, "1"); err != nil {
		t.Fatal(err)
	}
	value, ok, err := db.MemoryIndexMeta(ctx, client, db.MemoryIndexMetaNormalizer)
	if err != nil || !ok || value != "1" {
		t.Fatalf("meta=%q ok=%v err=%v", value, ok, err)
	}
	if err := db.SetMemoryIndexMeta(ctx, client, db.MemoryIndexMetaNormalizer, "2"); err != nil {
		t.Fatal(err)
	}
	if value, _, err := db.MemoryIndexMeta(ctx, client, db.MemoryIndexMetaNormalizer); err != nil || value != "2" {
		t.Fatalf("meta update=%q err=%v", value, err)
	}
}
