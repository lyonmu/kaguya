package db

import (
	"context"
	"fmt"

	"github.com/lyonmu/kaguya/internal/ent"
)

// 记忆 FTS5 检索设施的版本化初始化逻辑。常规表 kaguya_memory_search_doc 由 Ent
// schema 生成；这里的固定 SQL 只维护虚拟表、triggers 与索引元数据。索引结构变化时
// 递增 memoryFTSSchemaVersion，triggers 会被显式替换，不依赖 IF NOT EXISTS 升级。
const (
	memoryFTSSchemaVersion = "1"

	memoryIndexMetaTable = "kaguya_memory_index_meta"
	// MemoryIndexMetaFTSSchema 记录虚拟表 / trigger DDL 版本。
	MemoryIndexMetaFTSSchema = "fts_schema_version"
	// MemoryIndexMetaNormalizer 记录搜索投影的规范化算法版本；升级后需要从
	// 页面重新生成 SearchDoc，单执行 FTS rebuild 不能升级投影文本。
	MemoryIndexMetaNormalizer = "normalizer_version"
)

// memoryFTSSchema 是 external-content FTS：rowid 显式映射 SearchDoc 的整数主键。
// BM25 列权重（8/5/3/1）在检索 SQL 中按此列顺序指定。
const memoryFTSSchema = `
CREATE VIRTUAL TABLE kaguya_memory_fts USING fts5(
    title_terms, alias_terms, summary_terms, body_terms,
    content='kaguya_memory_search_doc',
    content_rowid='id',
    tokenize='unicode61',
    detail=full
);

CREATE TRIGGER kaguya_memory_search_ai
AFTER INSERT ON kaguya_memory_search_doc BEGIN
    INSERT INTO kaguya_memory_fts(
        rowid, title_terms, alias_terms, summary_terms, body_terms
    ) VALUES (
        new.id, new.title_terms, new.alias_terms, new.summary_terms, new.body_terms
    );
END;

CREATE TRIGGER kaguya_memory_search_ad
AFTER DELETE ON kaguya_memory_search_doc BEGIN
    INSERT INTO kaguya_memory_fts(
        kaguya_memory_fts, rowid, title_terms, alias_terms, summary_terms, body_terms
    ) VALUES (
        'delete', old.id, old.title_terms, old.alias_terms, old.summary_terms, old.body_terms
    );
END;

CREATE TRIGGER kaguya_memory_search_au
AFTER UPDATE ON kaguya_memory_search_doc BEGIN
    INSERT INTO kaguya_memory_fts(
        kaguya_memory_fts, rowid, title_terms, alias_terms, summary_terms, body_terms
    ) VALUES (
        'delete', old.id, old.title_terms, old.alias_terms, old.summary_terms, old.body_terms
    );
    INSERT INTO kaguya_memory_fts(
        rowid, title_terms, alias_terms, summary_terms, body_terms
    ) VALUES (
        new.id, new.title_terms, new.alias_terms, new.summary_terms, new.body_terms
    );
END;`

// EnsureMemoryFTS 在 Ent 常规表迁移后创建或升级 FTS5 设施：
// 保存索引 schema 版本，版本变化时显式替换虚拟表与 triggers，并用 rebuild +
// integrity-check 补建与校验已有投影。小库启动时同步执行，不在聊天请求路径维护。
func EnsureMemoryFTS(ctx context.Context, client *ent.Client) error {
	if _, err := client.ExecContext(ctx,
		"CREATE TABLE IF NOT EXISTS "+memoryIndexMetaTable+" (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		return fmt.Errorf("create memory index meta table: %w", err)
	}
	version, ok, err := MemoryIndexMeta(ctx, client, MemoryIndexMetaFTSSchema)
	if err != nil {
		return err
	}
	exists, err := memoryFTSExists(ctx, client)
	if err != nil {
		return err
	}
	if ok && version == memoryFTSSchemaVersion && exists {
		return nil
	}
	// 创建 triggers 不会补建已有内容的索引：整体重建投影索引并校验一致性。
	for _, statement := range []string{
		"DROP TRIGGER IF EXISTS kaguya_memory_search_ai",
		"DROP TRIGGER IF EXISTS kaguya_memory_search_ad",
		"DROP TRIGGER IF EXISTS kaguya_memory_search_au",
		"DROP TABLE IF EXISTS kaguya_memory_fts",
		memoryFTSSchema,
	} {
		if _, err := client.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate memory FTS schema: %w", err)
		}
	}
	if err := RebuildMemoryFTS(ctx, client); err != nil {
		return err
	}
	return SetMemoryIndexMeta(ctx, client, MemoryIndexMetaFTSSchema, memoryFTSSchemaVersion)
}

// RebuildMemoryFTS 从 SearchDoc 投影重建全文索引并执行完整性校验；
// 只能重新索引投影内容，不计算规范化文本。
func RebuildMemoryFTS(ctx context.Context, client *ent.Client) error {
	if _, err := client.ExecContext(ctx, "INSERT INTO kaguya_memory_fts(kaguya_memory_fts) VALUES('rebuild')"); err != nil {
		return fmt.Errorf("rebuild memory FTS index: %w", err)
	}
	if _, err := client.ExecContext(ctx, "INSERT INTO kaguya_memory_fts(kaguya_memory_fts, rank) VALUES('integrity-check', 1)"); err != nil {
		return fmt.Errorf("verify memory FTS index: %w", err)
	}
	return nil
}

// MemoryIndexMeta 读取索引元数据；key 不存在时 ok=false。
func MemoryIndexMeta(ctx context.Context, client *ent.Client, key string) (string, bool, error) {
	rows, err := client.QueryContext(ctx, "SELECT value FROM "+memoryIndexMetaTable+" WHERE key = ?", key)
	if err != nil {
		return "", false, fmt.Errorf("read memory index meta: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", false, fmt.Errorf("read memory index meta: %w", err)
		}
		return "", false, nil
	}
	var value string
	if err := rows.Scan(&value); err != nil {
		return "", false, fmt.Errorf("read memory index meta: %w", err)
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("read memory index meta: %w", err)
	}
	return value, true, nil
}

// SetMemoryIndexMeta 写入索引元数据。
func SetMemoryIndexMeta(ctx context.Context, client *ent.Client, key, value string) error {
	if _, err := client.ExecContext(ctx,
		"INSERT INTO "+memoryIndexMetaTable+" (key, value) VALUES(?, ?)"+
			" ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value); err != nil {
		return fmt.Errorf("write memory index meta: %w", err)
	}
	return nil
}

func memoryFTSExists(ctx context.Context, client *ent.Client) (bool, error) {
	rows, err := client.QueryContext(ctx,
		"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'kaguya_memory_fts'")
	if err != nil {
		return false, fmt.Errorf("inspect memory FTS schema: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, fmt.Errorf("inspect memory FTS schema: %w", err)
		}
		return false, fmt.Errorf("inspect memory FTS schema: no result")
	}
	var count int
	if err := rows.Scan(&count); err != nil {
		return false, fmt.Errorf("inspect memory FTS schema: %w", err)
	}
	return count > 0, rows.Err()
}
