package db_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/lyonmu/kaguya/internal/db"
)

// 模拟已有权限/审批表、但尚未加入执行审计和完整指纹的加密磁盘库。
// 原值使用 SQL 直接比较，避免 JSON 经 Ent 解码后丢失字节级差异。
func TestApprovalAuditMigrationPreservesExistingData(t *testing.T) {
	cfg, conn := legacyDatabase(t)
	ctx := context.Background()
	var cipherVersion string
	if err := conn.QueryRowContext(ctx, "PRAGMA cipher_version").Scan(&cipherVersion); err != nil || cipherVersion == "" {
		t.Fatalf("fixture must use SQLCipher: version=%q err=%v", cipherVersion, err)
	}
	execSQL(t, conn,
		"ALTER TABLE kaguya_approval DROP COLUMN expires_at",
		"ALTER TABLE kaguya_approval DROP COLUMN execution_state",
		"ALTER TABLE kaguya_approval DROP COLUMN execution_finished_at",
		"ALTER TABLE kaguya_approval DROP COLUMN execution_reason_code",
		"ALTER TABLE kaguya_chat_turn DROP COLUMN context_fingerprint_version",
		"ALTER TABLE kaguya_chat_turn DROP COLUMN context_fingerprint",
		"ALTER TABLE kaguya_chat_turn DROP COLUMN cache_creation_tokens",
		"ALTER TABLE kaguya_chat_turn DROP COLUMN preparation_usage",
		"DROP INDEX IF EXISTS kaguyaapproval_conversation_id_turn_id",
	)
	const stamp = "2026-01-01 00:00:00"
	insertMigrationRow(t, conn, "kaguya_project", "id,created_at,updated_at,name,path,description", "project-1", stamp, stamp, "旧项目", "/fixture/project", "")
	for i, snapshot := range []any{nil, "", "Global and project instructions\n保持原样"} {
		insertMigrationRow(t, conn, "kaguya_conversation",
			"id,created_at,updated_at,title,project_id,agent_instructions,project_instructions,instructions_format,permission_mode,last_message_at,model_id,model_name",
			fmt.Sprintf("conv-%d", i), stamp, stamp, "旧项目会话", "project-1", snapshot, snapshot, "legacy", "full_approval", stamp, "model-local", "模型")
	}
	insertMigrationRow(t, conn, "kaguya_conversation",
		"id,created_at,updated_at,title,last_message_at,model_id,model_name", "ordinary", stamp, stamp, "旧普通会话", stamp, "model-local", "模型")
	for i, status := range []string{"running", "completed", "interrupted", "failed", "canceled"} {
		id := fmt.Sprintf("turn-%d", i)
		insertMigrationRow(t, conn, "kaguya_chat_turn",
			"id,created_at,updated_at,conversation_id,turn_index,user_content,provider_id,provider_name,model_id,model_name,api_protocol,status,started_at,finished_at,duration_ms,tool_calls,finish_reason,input_tokens,output_tokens,total_tokens,cached_tokens,reasoning_tokens,messages,context_messages,permission_mode",
			id, stamp, stamp, "conv-0", i+1, "旧提问\n特殊字符 ' 和 \\", "provider-local", "提供商", "model-local", "模型", "openai", status, stamp, stamp, 10, 1, "stop", 13, 7, 20, 2, 1, "[ ]", "[]", "full_approval")
		insertMigrationRow(t, conn, "kaguya_chat_block",
			"id,created_at,updated_at,turn_id,sequence,type,text,tool_call_id,tool_name,input,started_at,finished_at,start_order,end_order",
			"block-"+id, stamp, stamp, id, 1, "tool_call", "旧正文", "call-"+id, "write", `{"path":"旧文件","content":"原内容"}`, stamp, stamp, 1, 2)
	}
	for i, status := range []string{"pending", "approved", "consumed", "rejected", "expired", "canceled"} {
		decision := ""
		if status == "approved" || status == "consumed" {
			decision = "approve"
		} else if status == "rejected" {
			decision = "reject"
		}
		insertMigrationRow(t, conn, "kaguya_approval",
			"id,created_at,updated_at,approval_id,conversation_id,turn_id,tool_call_id,input_hash,tool_identity,effect,mode,status,decision,requested_at,consumed_at,reason_code",
			fmt.Sprintf("row-%d", i), stamp, stamp, fmt.Sprintf("approval-%d", i), "conv-0", "turn-0", fmt.Sprintf("call-%d", i), strings.Repeat("a", 64), "mcp:fixture:write:1:schema", "unknown", "full_approval", status, decision, stamp, stamp, "old_reason")
	}
	for i, status := range []string{"pending", "running", "succeeded", "failed", "canceled"} {
		insertMigrationRow(t, conn, "kaguya_title_job",
			"id,created_at,updated_at,conversation_id,enqueue_order,status,question,answer,attempt_count,error_code,revision",
			fmt.Sprintf("job-%d", i), stamp, stamp, fmt.Sprintf("job-conv-%d", i), i+1, status, "原问题", "原回答", 3, "old_code", 9)
	}
	for i, failures := range []int{0, 2, 5} {
		insertMigrationRow(t, conn, "kaguya_mcp_server",
			"id,created_at,updated_at,name,transport,args,env,headers,enabled,reconnect_failures,reconnect_circuit_open,reconnect_cycle,reconnect_next_at,reconnect_opened_at",
			fmt.Sprintf("mcp-%d", i), stamp, stamp, fmt.Sprintf("旧服务-%d", i), "stdio", "[]", `{"FIXTURE":"fake-value"}`, `{"Authorization":"fake-value"}`, true, failures, failures == 5, 4, stamp, stamp)
	}
	// 保留软删除值，不依赖外键级联；迁移不能顺便恢复或清理历史。
	execSQL(t, conn, "UPDATE kaguya_approval SET deleted_at='2026-01-02 00:00:00' WHERE id='row-5'")
	queries := []string{
		"SELECT id,agent_instructions,project_instructions,project_id,instructions_format,permission_mode,title,deleted_at FROM kaguya_conversation ORDER BY id",
		"SELECT id,status,user_content,messages,context_messages,input_tokens,output_tokens,total_tokens,cached_tokens,reasoning_tokens FROM kaguya_chat_turn ORDER BY id",
		"SELECT id,text,input,tool_call_id,tool_name FROM kaguya_chat_block ORDER BY id",
		"SELECT id,approval_id,conversation_id,turn_id,tool_call_id,input_hash,tool_identity,status,decision,requested_at,consumed_at,reason_code,deleted_at FROM kaguya_approval ORDER BY id",
		"SELECT id,status,enqueue_order,question,answer,attempt_count,error_code,revision FROM kaguya_title_job ORDER BY id",
		"SELECT id,enabled,args,env,headers,reconnect_failures,reconnect_circuit_open,reconnect_cycle,reconnect_next_at,reconnect_opened_at FROM kaguya_mcp_server ORDER BY id",
	}
	before := make([][][]any, len(queries))
	for i, query := range queries {
		before[i] = migrationValues(t, conn, query)
	}
	for attempt := 0; attempt < 2; attempt++ {
		client, err := db.InitSQLite(cfg)
		if err != nil {
			t.Fatalf("migration %d: %v", attempt+1, err)
		}
		for i := 0; i < 6; i++ {
			row, err := client.KaguyaApproval.Get(ctx, fmt.Sprintf("row-%d", i))
			if err != nil || row.ExpiresAt != nil || row.ExecutionState != "not_started" || row.ExecutionFinishedAt != nil || row.ExecutionReasonCode != "" {
				t.Fatalf("migration %d fabricated approval execution/expiry: row=%d err=%v", attempt+1, i, err)
			}
		}
		turns, err := client.KaguyaChatTurn.Query().All(ctx)
		if err != nil || len(turns) != 5 {
			t.Fatalf("migration %d lost turns: err=%v", attempt+1, err)
		}
		for _, turn := range turns {
			if turn.ContextFingerprintVersion != 0 || turn.ContextFingerprint != nil || turn.CacheCreationTokens != 0 || turn.PreparationUsage != nil {
				t.Fatal("legacy context baseline acquired a fabricated fingerprint")
			}
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		for i, query := range queries {
			if !reflect.DeepEqual(before[i], migrationValues(t, conn, query)) {
				t.Fatalf("migration %d changed legacy values in fixture group %d", attempt+1, i)
			}
		}
	}
	// 新审计写入后再次升级，不能由默认值覆盖已记录的分项。
	const preparation = `{"version":1,"summary_calls":2,"reported_calls":1,"unreported_calls":1,"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":42,"cache_hit_tokens":5,"cache_creation_tokens":7,"reasoning_tokens":3}}`
	if _, err := conn.ExecContext(ctx, "UPDATE kaguya_chat_turn SET cache_creation_tokens=?, preparation_usage=? WHERE id='turn-1'", 7, preparation); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		client, err := db.InitSQLite(cfg)
		if err != nil {
			t.Fatal(err)
		}
		row, err := client.KaguyaChatTurn.Get(ctx, "turn-1")
		if err != nil || row.CacheCreationTokens != 7 || row.PreparationUsage == nil || row.PreparationUsage.Usage.CacheCreationTokens != 7 || row.PreparationUsage.UnreportedCalls != 1 {
			t.Fatalf("migration changed preparation audit: %v", err)
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		var raw string
		if err := conn.QueryRowContext(ctx, "SELECT preparation_usage FROM kaguya_chat_turn WHERE id='turn-1'").Scan(&raw); err != nil || raw != preparation {
			t.Fatal("migration rewrote original audit JSON")
		}
	}
	var foreignKeys int
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("connection foreign keys disabled: %d err=%v", foreignKeys, err)
	}
	for _, table := range []string{"kaguya_approval", "kaguya_chat_turn", "kaguya_title_job"} {
		if got := migrationValues(t, conn, "PRAGMA foreign_key_list("+table+")"); len(got) != 0 {
			t.Fatalf("migration added foreign key constraints to %s", table)
		}
	}
	var indexCount int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='index' AND tbl_name='kaguya_approval' AND sql LIKE '%conversation_id%turn_id%'`).Scan(&indexCount); err != nil || indexCount == 0 {
		t.Fatalf("approval history index missing: %d err=%v", indexCount, err)
	}
	const copyApproval = `INSERT INTO kaguya_approval (id,created_at,updated_at,approval_id,conversation_id,turn_id,tool_call_id,input_hash,tool_identity,effect,mode,status,decision,requested_at,reason_code)
		SELECT ?,created_at,updated_at,?,conversation_id,turn_id,?,input_hash,tool_identity,effect,mode,status,decision,requested_at,reason_code FROM kaguya_approval WHERE id='row-0'`
	for _, duplicate := range []struct{ id, approvalID, callID, column string }{
		{"duplicate-id", "approval-0", "new-call", "approval_id"},
		{"duplicate-call", "new-approval", "call-0", "tool_call_id"},
	} {
		_, err := conn.ExecContext(ctx, copyApproval, duplicate.id, duplicate.approvalID, duplicate.callID)
		if err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") || !strings.Contains(err.Error(), duplicate.column) {
			t.Fatalf("migration lost %s uniqueness: %v", duplicate.column, err)
		}
	}
	header, err := os.ReadFile(cfg.Path)
	if err != nil || strings.HasPrefix(string(header[:min(len(header), 16)]), "SQLite format 3") {
		t.Fatalf("fixture database is plaintext: err=%v", err)
	}
}

func insertMigrationRow(t *testing.T, conn *sql.DB, table, columns string, values ...any) {
	t.Helper()
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
	if _, err := conn.ExecContext(context.Background(), "INSERT INTO "+table+" ("+columns+") VALUES ("+placeholders+")", values...); err != nil {
		t.Fatalf("insert fixture table %s: %v", table, err)
	}
}

func migrationValues(t *testing.T, conn *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := conn.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range pointers {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for i, value := range values {
			if data, ok := value.([]byte); ok {
				values[i] = string(data)
			}
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
