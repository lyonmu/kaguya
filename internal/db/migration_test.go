package db_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/db"
)

// 旧库 fixture：模拟上一版本的表结构（无 split/权限/标题版本/MCP 熔断字段与
// 新表），验证 additive 迁移的默认值、旧数据不变与幂等。
func legacyDatabase(t *testing.T) (*config.DatabaseConfig, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	if err := os.WriteFile(keyFile, []byte(strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.DatabaseConfig{Path: filepath.Join(dir, "legacy.db"), KeyFile: keyFile}
	if err := cfg.EnsureSQLiteDatabase(); err != nil {
		t.Fatal(err)
	}
	client, err := db.InitSQLite(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	dsn, err := cfg.SQLiteDSN()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return cfg, conn
}

func execSQL(t *testing.T, conn *sql.DB, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := conn.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("exec %q: %v", statement, err)
		}
	}
}

func TestAdditiveMigrationPreservesLegacyData(t *testing.T) {
	cfg, conn := legacyDatabase(t)
	execSQL(t, conn,
		"DROP TABLE IF EXISTS kaguya_title_job",
		"DROP TABLE IF EXISTS kaguya_approval",
		"ALTER TABLE kaguya_conversation DROP COLUMN project_instructions",
		"ALTER TABLE kaguya_conversation DROP COLUMN instructions_format",
		"ALTER TABLE kaguya_conversation DROP COLUMN permission_mode",
		"ALTER TABLE kaguya_conversation DROP COLUMN title_revision",
		"ALTER TABLE kaguya_chat_turn DROP COLUMN permission_mode",
		"ALTER TABLE kaguya_chat_turn DROP COLUMN global_instruction_hash",
		"ALTER TABLE kaguya_chat_turn DROP COLUMN global_instruction_version",
		"ALTER TABLE kaguya_chat_turn DROP COLUMN reference_summary",
		"ALTER TABLE kaguya_mcp_server DROP COLUMN reconnect_failures",
		"ALTER TABLE kaguya_mcp_server DROP COLUMN reconnect_circuit_open",
		"ALTER TABLE kaguya_mcp_server DROP COLUMN reconnect_cycle",
		"ALTER TABLE kaguya_mcp_server DROP COLUMN reconnect_next_at",
		"ALTER TABLE kaguya_mcp_server DROP COLUMN reconnect_opened_at",
	)
	const legacyInstructions = "Global instructions from /home/user/.config/agents/AGENTS.md:\n\nkeep this text byte for byte"
	execSQL(t, conn,
		`INSERT INTO kaguya_conversation (id, created_at, updated_at, title, agent_instructions, favorite, memory_mode, turn_count, last_message_at, model_id, model_name, duration_ms, tool_calls, input_tokens, output_tokens, total_tokens, cached_tokens, reasoning_tokens)
		 VALUES ('legacy-1', '2026-01-01 00:00:00', '2026-01-01 00:00:00', '旧会话', '`+legacyInstructions+`', 1, 'inherit', 1, '2026-01-01 00:00:01', 'model-a', 'Model A', 100, 0, 10, 20, 30, 0, 0)`,
		`INSERT INTO kaguya_chat_turn (id, created_at, updated_at, turn_index, user_content, provider_id, provider_name, model_id, model_name, api_protocol, status, started_at, finished_at, duration_ms, tool_calls, finish_reason, input_tokens, output_tokens, total_tokens, cached_tokens, reasoning_tokens, messages, conversation_id)
		 VALUES ('turn-1', '2026-01-01 00:00:00', '2026-01-01 00:00:00', 1, '旧提问', 'p1', 'Provider', 'model-a', 'Model A', 'openai', 'completed', '2026-01-01 00:00:00', '2026-01-01 00:00:01', 1000, 0, 'stop', 10, 20, 30, 0, 0, '[]', 'legacy-1')`,
	)

	// 迁移两次：第一次补齐字段，第二次必须幂等且不改变旧文本。
	for attempt := 0; attempt < 2; attempt++ {
		client, err := db.InitSQLite(cfg)
		if err != nil {
			t.Fatalf("migration attempt %d: %v", attempt+1, err)
		}
		ctx := context.Background()
		row, err := client.KaguyaConversation.Get(ctx, "legacy-1")
		if err != nil {
			t.Fatal(err)
		}
		if row.PermissionMode != "full_access" || row.InstructionsFormat != "legacy" || row.TitleRevision != 0 {
			t.Fatalf("unexpected migration defaults: %+v", row)
		}
		if row.ProjectInstructions != nil {
			t.Fatal("legacy project instructions must stay NULL")
		}
		if row.AgentInstructions == nil || *row.AgentInstructions != legacyInstructions {
			t.Fatalf("legacy instructions changed: %v", row.AgentInstructions)
		}
		turn, err := client.KaguyaChatTurn.Get(ctx, "turn-1")
		if err != nil {
			t.Fatal(err)
		}
		if turn.PermissionMode != "full_access" || turn.UserContent != "旧提问" {
			t.Fatalf("unexpected turn after migration: %+v", turn)
		}
		if attempt == 1 {
			// 新表存在且默认值正确。
			count, err := client.KaguyaTitleJob.Query().Count(ctx)
			if err != nil || count != 0 {
				t.Fatalf("title job table count=%d err=%v", count, err)
			}
			if _, err := client.KaguyaApproval.Query().Count(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// 新约束：同一会话一行标题任务；同一轮同一工具调用一行审批。
	execSQL(t, conn,
		`INSERT INTO kaguya_title_job (id, created_at, updated_at, conversation_id, enqueue_order, status, question, answer, attempt_count, error_code, revision)
		 VALUES ('job-1', '2026-01-01 00:00:00', '2026-01-01 00:00:00', 'legacy-1', 1, 'pending', 'q', '', 0, '', 0)`,
	)
	if _, err := conn.ExecContext(context.Background(),
		`INSERT INTO kaguya_title_job (id, created_at, updated_at, conversation_id, enqueue_order, status, question, answer, attempt_count, error_code, revision)
		 VALUES ('job-2', '2026-01-01 00:00:01', '2026-01-01 00:00:01', 'legacy-1', 2, 'pending', 'q', '', 0, '', 0)`); err == nil {
		t.Fatal("unique conversation_id constraint missing on kaguya_title_job")
	}
	execSQL(t, conn,
		`INSERT INTO kaguya_approval (id, created_at, updated_at, approval_id, conversation_id, turn_id, tool_call_id, input_hash, tool_identity, effect, mode, status, decision, requested_at, reason_code)
		 VALUES ('ap-1', '2026-01-01 00:00:00', '2026-01-01 00:00:00', 'approval-1', 'legacy-1', 'turn-1', 'call-1', 'hash', 'read', 'read_only', 'full_approval', 'pending', '', '2026-01-01 00:00:00', '')`,
	)
	if _, err := conn.ExecContext(context.Background(),
		`INSERT INTO kaguya_approval (id, created_at, updated_at, approval_id, conversation_id, turn_id, tool_call_id, input_hash, tool_identity, effect, mode, status, decision, requested_at, reason_code)
		 VALUES ('ap-2', '2026-01-01 00:00:01', '2026-01-01 00:00:01', 'approval-2', 'legacy-1', 'turn-1', 'call-1', 'hash', 'read', 'read_only', 'full_approval', 'pending', '', '2026-01-01 00:00:00', '')`); err == nil {
		t.Fatal("unique turn_id+tool_call_id constraint missing on kaguya_approval")
	}
}
