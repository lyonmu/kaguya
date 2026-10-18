package db_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/internal/secret"
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
	"github.com/mattn/go-sqlite3"
	"go.uber.org/zap"
)

// 历史结构直接来自对应提交的 Ent Tables，而非先建当前库再删字段。
//
//go:embed testdata/legacy-pre-eight.sql testdata/legacy-pre-hardening.sql testdata/legacy-memory-fts.sql
var restoreSchemas embed.FS

type restoreSnapshot struct {
	query   string
	columns []string
	rows    [][]any
}

type restoreBackup struct {
	cfg    *config.DatabaseConfig
	files  map[string][32]byte
	length map[string]int
}

// 关闭全部连接后才复制；覆盖旧版本和升级后的备份，以及真实保留/移除sidecar。
// 此测试验证DB及service恢复入口；完整cmd/runtime和实际工具不重放另行验收。
func TestHistoricalSQLCipherBackupRestore(t *testing.T) {
	for _, schema := range []struct {
		name, digest string
		approval     bool
	}{
		{"pre-eight", "bd52bb4be44dcf1d48a8ef255b1c4a4a5b327f5c5515f9eb2579ad708006a55d", false},
		{"pre-hardening", "35f1c5087b9bcba5144903d6034a433269627fb270fbe1ae4d443ad5cdc3de16", true},
	} {
		for _, persistWAL := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/persist_wal_%t", schema.name, persistWAL), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				var mcpRequests atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					mcpRequests.Add(1)
					w.WriteHeader(http.StatusServiceUnavailable)
				}))
				defer server.Close()
				cfg := restoreConfig(t)
				pool := restoreOpen(t, cfg)
				ddl, err := restoreSchemas.ReadFile("testdata/legacy-" + schema.name + ".sql")
				if err != nil {
					t.Fatal(err)
				}
				start := bytes.Index(ddl, []byte("\nCREATE TABLE"))
				if start >= 0 {
					start++
				}
				if start < 0 || fmt.Sprintf("%x", sha256.Sum256(ddl[start:])) != schema.digest {
					t.Fatal("historical DDL does not match its recorded source fixture")
				}
				if _, err := pool.ExecContext(ctx, string(ddl)); err != nil {
					t.Fatalf("create historical schema: %v", err)
				}
				fts, err := restoreSchemas.ReadFile("testdata/legacy-memory-fts.sql")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := pool.ExecContext(ctx, string(fts)); err != nil {
					t.Fatalf("create historical derived FTS: %v", err)
				}
				restoreSeed(t, pool, cfg, schema.approval, server.URL)
				original := restoreCapture(t, pool)
				restoreClose(t, pool, persistWAL)
				oldBackup := restoreCopy(t, cfg, persistWAL)

				// 升级前备份先恢复为工作副本，升级不能触及唯一旧版本备份。
				working := restoreCopy(t, oldBackup.cfg, persistWAL)
				for attempt := 0; attempt < 2; attempt++ {
					restoreMigrate(t, working.cfg, schema.approval)
					restoreCompare(t, working.cfg, original)
				}
				pool = restoreOpen(t, working.cfg)
				restoreClose(t, pool, persistWAL)
				newBackup := restoreCopy(t, working.cfg, persistWAL)
				restored := restoreCopy(t, newBackup.cfg, persistWAL)
				for attempt := 0; attempt < 2; attempt++ {
					restoreMigrate(t, restored.cfg, schema.approval)
					restoreCompare(t, restored.cfg, original)
				}
				restoreReconcile(t, ctx, restored.cfg, schema.approval)
				if mcpRequests.Load() != 0 {
					t.Fatal("restored circuit-open/disabled MCP made an automatic request")
				}
				restoreFailureCopies(t, newBackup, persistWAL, schema.approval)
				restoreBackupUnchanged(t, oldBackup)
				restoreBackupUnchanged(t, newBackup)

				// 验证旧备份仍能由原生引擎解密、保持旧结构与全部旧值。
				// 这不是宣称已运行旧版应用或已完成版本降级。
				rollback := restoreCopy(t, oldBackup.cfg, persistWAL)
				pool = restoreOpen(t, rollback.cfg)
				if !reflect.DeepEqual(original, restoreCapture(t, pool)) {
					t.Fatal("old-version backup no longer contains its original schema/data")
				}
				restoreClose(t, pool, false)
				t.Logf("RESTORE schema=%s retained_sidecars=%t old_files=%d new_files=%d migrations=4 service_reconciliations=2 bad_copies=3 mcp_requests=%d", schema.name, persistWAL, len(oldBackup.files), len(newBackup.files), mcpRequests.Load())
			})
		}
	}
}

func restoreConfig(t *testing.T) *config.DatabaseConfig {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.DatabaseConfig{Path: filepath.Join(dir, "legacy.db"), KeyFile: filepath.Join(dir, "key")}
	if err := os.WriteFile(cfg.KeyFile, []byte(strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureSQLiteDatabase(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func restoreOpen(t *testing.T, cfg *config.DatabaseConfig) *sql.DB {
	t.Helper()
	dsn, err := cfg.SQLiteDSN()
	if err != nil {
		t.Fatal(err)
	}
	pool, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal("open fixture driver failed")
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	var version string
	if err := pool.QueryRow("PRAGMA cipher_version").Scan(&version); err != nil || version == "" {
		t.Fatal("native SQLCipher fixture is required")
	}
	return pool
}

func restoreColumns(t *testing.T, pool *sql.DB, table string) []string {
	t.Helper()
	var columns []string
	for _, row := range migrationValues(t, pool, "PRAGMA table_info("+restoreQuote(table)+")") {
		columns = append(columns, row[1].(string))
	}
	return columns
}

func restoreQuote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func restoreCapture(t *testing.T, pool *sql.DB) map[string]restoreSnapshot {
	t.Helper()
	result := map[string]restoreSnapshot{}
	// FTS虚拟表及shadow由版本化派生逻辑校验，不当作拥有业务id的Ent表。
	for _, row := range migrationValues(t, pool, "SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'kaguya_%' AND name NOT GLOB 'kaguya_memory_fts*' ORDER BY name") {
		table := row[0].(string)
		columns := restoreColumns(t, pool, table)
		quoted := make([]string, len(columns))
		for i := range columns {
			quoted[i] = restoreQuote(columns[i])
		}
		order := restoreQuote(columns[0]) // index_meta使用key；Ent表首列为id。
		query := "SELECT " + strings.Join(quoted, ",") + " FROM " + restoreQuote(table) + " ORDER BY " + order
		result[table] = restoreSnapshot{query: query, columns: columns, rows: migrationValues(t, pool, query)}
	}
	return result
}

func restoreCompare(t *testing.T, cfg *config.DatabaseConfig, before map[string]restoreSnapshot) {
	t.Helper()
	pool := restoreOpen(t, cfg)
	defer pool.Close()
	for table, snapshot := range before {
		if !reflect.DeepEqual(snapshot.rows, migrationValues(t, pool, snapshot.query)) {
			t.Fatalf("migration/restore changed original columns in %s", table)
		}
	}
	var integrity string
	if err := pool.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("restored integrity check failed: %v", err)
	}
	var foreignKeys int
	if err := pool.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatal("connection foreign keys must remain enabled")
	}
	var matched int
	if err := pool.QueryRow("SELECT count(*) FROM kaguya_memory_fts WHERE kaguya_memory_fts MATCH 'restorefixture'").Scan(&matched); err != nil || matched != 1 {
		t.Fatal("historical FTS search projection did not survive migration/restore")
	}
}

// 只插入该历史结构真实拥有的字段；两版差异在测试开头另行检查。
func restoreInsert(t *testing.T, pool *sql.DB, table string, values map[string]any) {
	t.Helper()
	available := restoreColumns(t, pool, table)
	var columns []string
	var args []any
	for _, name := range available {
		if value, ok := values[name]; ok {
			columns = append(columns, name)
			args = append(args, value)
		}
	}
	insertMigrationRow(t, pool, table, strings.Join(columns, ","), args...)
}

func restoreSeed(t *testing.T, pool *sql.DB, cfg *config.DatabaseConfig, approval bool, mcpURL string) {
	t.Helper()
	const stamp = "2026-01-01 00:00:00"
	base := func(id string) map[string]any {
		return map[string]any{"id": id, "created_at": stamp, "updated_at": stamp}
	}
	insert := func(table, id string, fields map[string]any) {
		values := base(id)
		for name, value := range fields {
			values[name] = value
		}
		restoreInsert(t, pool, table, values)
	}
	// 在建数据前证明fixture确实区分两代结构，而非当前schema的删字段副本。
	oldColumns := strings.Join(restoreColumns(t, pool, "kaguya_chat_turn"), ",")
	if strings.Contains(oldColumns, "context_fingerprint") || strings.Contains(oldColumns, "preparation_usage") || strings.Contains(oldColumns, "reference_requested") {
		t.Fatal("historical turn fixture contains current hardening columns")
	}
	if (len(restoreColumns(t, pool, "kaguya_approval")) > 0) != approval {
		t.Fatal("historical approval table presence is wrong")
	}
	key, err := cfg.SQLCipherKeyBytes()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.NewCipher("", key)
	if err != nil {
		t.Fatal(err)
	}
	providerKey, err := cipher.Encrypt("fixture-provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	insert("kaguya_provider_info", "provider-1", map[string]any{"provider_name": "旧提供商", "provider_type": "normal", "api_key": providerKey, "base_url": "https://fixture.invalid"})
	insert("kaguya_provider_info", "provider-deleted", map[string]any{"provider_name": "已删除提供商", "deleted_at": stamp, "api_key": providerKey})
	insert("kaguya_models_info", "model-local", map[string]any{"provider_id": "provider-1", "model_name": "旧模型", "model_id": "upstream-fixture", "api_protocol": "openai-chat", "token_context_window": 8192})
	insert("kaguya_project", "project-1", map[string]any{"name": "旧项目", "path": "/fixture/project", "description": "不操作该路径"})
	insert("kaguya_memory_page", "legacy-page", map[string]any{"scope_key": "global", "canonical_key": "restorefixture", "kind": "fact", "title": "恢复记忆", "summary": "原摘要", "body": "原记忆正文", "aliases": `[ "restorefixture" ]`, "status": "active", "version": 2})
	restoreInsert(t, pool, "kaguya_memory_search_doc", map[string]any{"id": int64(123), "page_id": "legacy-page", "page_version": 2, "normalizer_version": 2, "title_terms": "restorefixture", "body_terms": "originalbody"})
	for i, snapshot := range []any{nil, "", "Global instructions from fixture\n\nProject instructions from fixture\n保持原样 ' \\"} {
		insert("kaguya_conversation", fmt.Sprintf("conv-%d", i), map[string]any{"title": "旧项目会话", "project_id": "project-1", "agent_instructions": snapshot, "project_instructions": snapshot, "permission_mode": "full_approval", "instructions_format": "legacy", "last_message_at": stamp, "model_id": "upstream-fixture", "model_name": "旧模型", "turn_count": 1, "total_tokens": 20})
	}
	insert("kaguya_conversation", "ordinary", map[string]any{"title": "旧普通会话", "last_message_at": stamp, "model_id": "upstream-fixture", "model_name": "旧模型"})
	insert("kaguya_conversation", "deleted", map[string]any{"title": "已删除会话", "deleted_at": stamp, "last_message_at": stamp, "model_id": "upstream-fixture", "model_name": "旧模型"})
	for i, status := range []string{"running", "completed", "interrupted", "failed", "canceled"} {
		id := fmt.Sprintf("turn-%d", i)
		messages, err := json.MarshalIndent([]fantasy.Message{
			fantasy.NewUserMessage("旧提问\n'特殊字符\\"),
			{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.ToolCallPart{ToolCallID: "call-" + id, ToolName: "write", Input: `{ "path": "旧文件", "content": "原内容" }`}}},
			{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.ToolResultPart{ToolCallID: "call-" + id, Output: fantasy.ToolResultOutputContentText{Text: "旧结果"}}}},
		}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		insert("kaguya_chat_turn", id, map[string]any{"conversation_id": "conv-0", "turn_index": i + 1, "user_content": "旧提问\n'特殊字符\\", "provider_id": "provider-1", "provider_name": "旧提供商", "model_id": "upstream-fixture", "model_name": "旧模型", "api_protocol": "openai-chat", "status": status, "started_at": stamp, "finished_at": stamp, "duration_ms": 10, "tool_calls": 1, "finish_reason": "stop", "input_tokens": 13, "output_tokens": 7, "total_tokens": 20, "cached_tokens": 2, "reasoning_tokens": 1, "messages": string(messages), "context_messages": "\n" + string(messages) + "\n", "memory_refs": `{ "refs": [] }`, "permission_mode": "full_approval", "global_instruction_hash": strings.Repeat("a", 64), "global_instruction_version": 4, "reference_summary": `{ "sources": [] }`})
		insert("kaguya_chat_block", "block-"+id, map[string]any{"turn_id": id, "sequence": 1, "type": "tool_call", "text": "原工具调用", "tool_call_id": "call-" + id, "tool_name": "write", "input": `{ "path": "旧文件", "content": "原内容" }`, "started_at": stamp, "finished_at": stamp, "start_order": 1, "end_order": 2})
		insert("kaguya_chat_block", "result-"+id, map[string]any{"turn_id": id, "sequence": 2, "type": "tool_result", "tool_call_id": "call-" + id, "tool_name": "write", "output": `{ "kind": "text", "text": "旧结果" }`, "started_at": stamp, "finished_at": stamp, "start_order": 3, "end_order": 4})
	}
	insert("kaguya_task_usage", "usage-1", map[string]any{"kind": "title", "conversation_id": "conv-0", "provider_id": "provider-1", "provider_name": "旧提供商", "model_id": "upstream:upstream-fixture", "model_name": "旧模型", "finished_at": stamp, "usage_known": true, "input_tokens": 4, "output_tokens": 6, "total_tokens": 10})
	insert("kaguya_mcp_server", "mcp-1", map[string]any{"name": "旧服务", "transport": "streamable-http", "url": mcpURL, "args": "[ ]", "env": `{ "FIXTURE": "fake-env" }`, "headers": `{ "Authorization": "fake-header" }`, "enabled": approval, "reconnect_failures": 5, "reconnect_circuit_open": true, "reconnect_cycle": 4, "reconnect_next_at": stamp, "reconnect_opened_at": stamp})
	if !approval {
		return
	}
	for i, status := range []string{"pending", "approved", "consumed", "rejected", "expired", "canceled"} {
		decision := ""
		if status == "approved" || status == "consumed" {
			decision = "approve"
		} else if status == "rejected" {
			decision = "reject"
		}
		insert("kaguya_approval", fmt.Sprintf("approval-row-%d", i), map[string]any{"approval_id": fmt.Sprintf("approval-%d", i), "conversation_id": "conv-0", "turn_id": "turn-0", "tool_call_id": fmt.Sprintf("approval-call-%d", i), "input_hash": strings.Repeat("a", 64), "tool_identity": "builtin:write", "effect": "mutating", "mode": "full_approval", "status": status, "decision": decision, "requested_at": stamp, "consumed_at": stamp, "reason_code": "old_reason"})
	}
	for i, status := range []string{"pending", "running", "succeeded", "failed", "canceled"} {
		conversationID := fmt.Sprintf("title-conv-%d", i)
		insert("kaguya_conversation", conversationID, map[string]any{"title": "新对话", "last_message_at": stamp, "model_id": "upstream-fixture", "model_name": "旧模型"})
		insert("kaguya_title_job", fmt.Sprintf("job-%d", i), map[string]any{"conversation_id": conversationID, "enqueue_order": i + 1, "status": status, "question": "原问题", "answer": "原回答", "attempt_count": 3, "error_code": "old_code", "revision": 9, "started_at": stamp, "finished_at": stamp})
	}
}

func restoreClose(t *testing.T, pool *sql.DB, persistWAL bool) {
	t.Helper()
	if persistWAL {
		conn, err := pool.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		err = conn.Raw(func(driver any) error {
			native, ok := driver.(*sqlite3.SQLiteConn)
			if !ok {
				return errors.New("fixture is not native SQLite")
			}
			return native.SetFileControlInt("main", sqlite3.SQLITE_FCNTL_PERSIST_WAL, 1)
		})
		_ = conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		// 测试专用metadata写入，确保生成真实WAL；最后Close可能已checkpoint。
		if _, err := pool.Exec("PRAGMA user_version=43"); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
}

func restoreCopy(t *testing.T, cfg *config.DatabaseConfig, persistWAL bool) restoreBackup {
	t.Helper()
	dir := t.TempDir()
	copyCfg := &config.DatabaseConfig{Path: filepath.Join(dir, "legacy.db"), KeyFile: filepath.Join(dir, "key")}
	backup := restoreBackup{cfg: copyCfg, files: map[string][32]byte{}, length: map[string]int{}}
	for _, file := range []struct{ source, target string }{{cfg.Path, copyCfg.Path}, {cfg.KeyFile, copyCfg.KeyFile}, {cfg.Path + "-wal", copyCfg.Path + "-wal"}, {cfg.Path + "-shm", copyCfg.Path + "-shm"}} {
		data, err := os.ReadFile(file.source)
		if errors.Is(err, os.ErrNotExist) && (strings.HasSuffix(file.source, "-wal") || strings.HasSuffix(file.source, "-shm")) {
			if persistWAL {
				t.Fatal("requested retained sidecar is absent after closing all fixture connections")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(file.source, "-wal") || strings.HasSuffix(file.source, "-shm") {
			if !persistWAL || len(data) == 0 {
				t.Fatal("unexpected or empty sidecar in closed fixture")
			}
		}
		if file.source == cfg.Path && bytes.HasPrefix(data, []byte("SQLite format 3")) {
			t.Fatal("backup is plaintext")
		}
		if err := os.WriteFile(file.target, data, 0o600); err != nil {
			t.Fatal(err)
		}
		backup.files[file.target] = sha256.Sum256(data)
		backup.length[file.target] = len(data)
	}
	return backup
}

func restoreBackupUnchanged(t *testing.T, backup restoreBackup) {
	t.Helper()
	for file, digest := range backup.files {
		data, err := os.ReadFile(file)
		if err != nil || sha256.Sum256(data) != digest || len(data) != backup.length[file] {
			t.Fatal("original backup was changed by a restore attempt")
		}
	}
}

func restoreMigrate(t *testing.T, cfg *config.DatabaseConfig, approval bool) {
	t.Helper()
	client, err := db.InitSQLite(cfg)
	if err != nil {
		t.Fatalf("upgrade historical encrypted fixture: %v", err)
	}
	defer client.Close()
	ctx := context.Background()
	turns, err := client.KaguyaChatTurn.Query().All(ctx)
	if err != nil || len(turns) != 5 {
		t.Fatal("migration lost historical turns")
	}
	for _, turn := range turns {
		if turn.ContextFingerprintVersion != 0 || turn.ContextFingerprint != nil || turn.CacheCreationTokens != 0 || turn.PreparationUsage != nil {
			t.Fatal("migration fabricated context/preparation data")
		}
	}
	rows, err := client.KaguyaApproval.Query().All(ctx)
	if err != nil || (approval && len(rows) != 6) || (!approval && len(rows) != 0) {
		t.Fatal("migration changed approval rows")
	}
	for _, row := range rows {
		if row.ExpiresAt != nil || row.ExecutionState != "not_started" || row.ExecutionFinishedAt != nil || row.ExecutionReasonCode != "" {
			t.Fatal("migration fabricated historical execution results")
		}
	}
	key, err := cfg.SQLCipherKeyBytes()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.NewCipher("", key)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := client.KaguyaProviderInfo.Get(ctx, "provider-1")
	if err != nil || provider.APIKey == "" {
		t.Fatal("restored provider key is missing")
	}
	clear, err := cipher.Decrypt(provider.APIKey)
	if err != nil || clear != "fixture-provider-secret" {
		t.Fatal("restored provider key cannot be decrypted with the matching master key")
	}
	version, exists, err := db.MemoryIndexMeta(ctx, client, db.MemoryIndexMetaFTSSchema)
	if err != nil || !exists || version != "1" {
		t.Fatal("restored derived FTS version is missing")
	}
	if _, err := client.ExecContext(ctx, "INSERT INTO kaguya_memory_fts(kaguya_memory_fts, rank) VALUES('integrity-check', 1)"); err != nil {
		t.Fatal("restored derived FTS integrity check failed")
	}
}

func restoreReconcile(t *testing.T, ctx context.Context, cfg *config.DatabaseConfig, approval bool) {
	t.Helper()
	client, err := db.InitSQLite(cfg)
	if err != nil {
		t.Fatal(err)
	}
	oldClient, oldLogger := db.EntClient, global.Logger
	oldPublisher, oldManager, oldRecovery := serviceagent.DefaultTitlePublisher, agentmcp.Default, servicesystem.DefaultMCPRecovery
	publisher, manager := serviceagent.NewTitlePublisher(), agentmcp.NewManager()
	db.EntClient, global.Logger = client, zap.NewNop()
	serviceagent.DefaultTitlePublisher, agentmcp.Default = publisher, manager
	servicesystem.DefaultMCPRecovery = servicesystem.NewMCPRecoveryCoordinator()
	defer func() {
		publisher.Close()
		manager.Close()
		_ = client.Close()
		db.EntClient, global.Logger = oldClient, oldLogger
		serviceagent.DefaultTitlePublisher, agentmcp.Default, servicesystem.DefaultMCPRecovery = oldPublisher, oldManager, oldRecovery
	}()
	pool := restoreOpen(t, cfg)
	original := restoreCapture(t, pool)
	_ = pool.Close()
	var first map[string]restoreSnapshot
	for attempt := 0; attempt < 2; attempt++ {
		for _, reconcile := range []func(context.Context) error{serviceagent.ReconcileRunningTurns, serviceagent.ReconcileTitleJobs, serviceagent.ReconcileApprovals} {
			if err := reconcile(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if err := servicesystem.NewSystemSvc(ctx).RestoreMCP(ctx); err != nil {
			t.Fatal(err)
		}
		assertRestoredStates(t, ctx, client, approval)
		pool := restoreOpen(t, cfg)
		current := restoreCapture(t, pool)
		_ = pool.Close()
		if attempt == 0 {
			restoreRecoveryPreserved(t, original, current)
			first = current
		} else if !reflect.DeepEqual(first, current) {
			t.Fatal("second service reconciliation changed terminal recovered state")
		}
	}
}

// 仅指定身份的恢复字段允许变化；其余原始JSON、输入、结果、用量、归属逐列保持。
func restoreRecoveryPreserved(t *testing.T, before, after map[string]restoreSnapshot) {
	t.Helper()
	for table, snapshot := range before {
		current, ok := after[table]
		if !ok || !reflect.DeepEqual(snapshot.columns, current.columns) || len(snapshot.rows) != len(current.rows) {
			t.Fatalf("recovery changed the structure/row count of %s", table)
		}
		for i, row := range snapshot.rows {
			id, _ := row[0].(string) // SearchDoc是整数id；只对指定字符串id开放恢复变更。
			for columnIndex, name := range snapshot.columns {
				allowed := false
				switch {
				case table == "kaguya_chat_turn" && id == "turn-0":
					allowed = name == "status" || name == "finished_at" || name == "duration_ms" || name == "updated_at"
				case table == "kaguya_approval" && (id == "approval-row-0" || id == "approval-row-1"):
					allowed = name == "status" || name == "reason_code" || name == "resolved_at" || name == "updated_at"
				case table == "kaguya_title_job" && id == "job-1":
					allowed = name == "status" || name == "revision" || name == "started_at" || name == "finished_at" || name == "error_code" || name == "updated_at"
				}
				if !allowed && !reflect.DeepEqual(row[columnIndex], current.rows[i][columnIndex]) {
					t.Fatalf("startup recovery changed preserved column %s.%s", table, name)
				}
			}
		}
	}
}

func assertRestoredStates(t *testing.T, ctx context.Context, client *ent.Client, approval bool) {
	t.Helper()
	for i, status := range []string{"interrupted", "completed", "interrupted", "failed", "canceled"} {
		row, err := client.KaguyaChatTurn.Get(ctx, fmt.Sprintf("turn-%d", i))
		if err != nil || string(row.Status) != status || row.UserContent != "旧提问\n'特殊字符\\" {
			t.Fatal("running-turn recovery lost user input or changed a terminal status")
		}
	}
	if !approval {
		return
	}
	for i, status := range []string{"canceled", "canceled", "consumed", "rejected", "expired", "canceled"} {
		row, err := client.KaguyaApproval.Get(ctx, fmt.Sprintf("approval-row-%d", i))
		if err != nil || string(row.Status) != status || row.ExecutionFinishedAt != nil {
			t.Fatal("approval recovery fabricated execution or retained unconsumed authorization")
		}
		if i < 2 && row.ReasonCode != "startup_recovery" {
			t.Fatal("unconsumed approval has no recovery reason")
		}
	}
	svc := &serviceagent.AgentSvc{}
	pending, err := svc.ConversationApprovals(ctx, "conv-0")
	if err != nil || len(pending) != 0 {
		t.Fatal("restored process reconstructed pending approval waiters")
	}
	for i := 0; i < 2; i++ {
		if _, err := svc.DecideApproval(ctx, "conv-0", fmt.Sprintf("approval-%d", i), "approve"); !errors.Is(err, serviceagent.ErrApprovalConflict) {
			t.Fatal("startup-canceled approval can be approved again")
		}
	}
	history, err := svc.ConversationTurns(ctx, "conv-0", &dtochat.TurnPageReq{Limit: 10})
	if err != nil || len(history.Items) != 5 {
		t.Fatalf("restored history unavailable: %v", err)
	}
	var consumedUnknown bool
	for _, turn := range history.Items {
		for _, row := range turn.Approvals {
			if row.ApprovalID == "approval-2" {
				consumedUnknown = row.Status == "consumed" && row.ExecutionState == "unknown" && row.ExecutionFinishedAt == 0
			}
		}
	}
	if !consumedUnknown {
		t.Fatal("legacy consumed approval is not displayed as unknown")
	}
	jobs, err := client.KaguyaTitleJob.Query().All(ctx)
	if err != nil || len(jobs) != 5 {
		t.Fatal("restored durable title jobs missing")
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].EnqueueOrder < jobs[j].EnqueueOrder })
	for i, status := range []string{"pending", "pending", "succeeded", "failed", "canceled"} {
		job := jobs[i]
		revision := int64(9)
		if i == 1 {
			revision++
			if job.StartedAt != nil || job.FinishedAt != nil || job.ErrorCode != "" {
				t.Fatal("running title job was not reset for a new claim")
			}
		}
		if string(job.Status) != status || job.Revision != revision || job.Question != "原问题" || job.Answer != "原回答" {
			t.Fatal("title recovery lost persisted source or changed terminal revision")
		}
	}
	if servicesystem.DefaultMCPRecovery.Allowed("mcp-1", time.Now()) {
		t.Fatal("persistent MCP open circuit was cleared by restore")
	}
}

func restoreFailureCopies(t *testing.T, original restoreBackup, persistWAL, approval bool) {
	t.Helper()
	for _, failure := range []string{"missing_key", "wrong_key", "damaged_database"} {
		t.Run(failure, func(t *testing.T) {
			attempt := restoreCopy(t, original.cfg, persistWAL)
			var err error
			switch failure {
			case "missing_key":
				err = os.Remove(attempt.cfg.KeyFile)
			case "wrong_key":
				err = os.WriteFile(attempt.cfg.KeyFile, []byte(strings.Repeat("cd", 32)+"\n"), 0o600)
			case "damaged_database":
				var file *os.File
				file, err = os.OpenFile(attempt.cfg.Path, os.O_WRONLY, 0)
				if err == nil {
					_, err = file.WriteAt(make([]byte, 4096), 0)
					_ = file.Close()
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			client, err := db.InitSQLite(attempt.cfg)
			if client != nil {
				_ = client.Close()
			}
			if err == nil {
				t.Fatal("invalid restore copy was silently accepted")
			}
			if strings.Contains(err.Error(), strings.Repeat("cd", 32)) || strings.Contains(err.Error(), "key=x") {
				t.Fatal("restore error exposed key/DSN")
			}
			if failure == "missing_key" {
				if _, err := os.Stat(attempt.cfg.KeyFile); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("restore generated a replacement key")
				}
			}
			restoreBackupUnchanged(t, original)
		})
	}
	valid := restoreCopy(t, original.cfg, persistWAL)
	restoreMigrate(t, valid.cfg, approval)
}
