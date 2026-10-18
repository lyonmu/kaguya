package cmd

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lyonmu/kaguya/internal/agent/permission"
	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaapproval"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/internal/secret"
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
	"github.com/mattn/go-sqlite3"
)

var (
	runtimeRestoreFixture = flag.String("runtime-restore-fixture", "", "private fixture directory for the restore test subprocess")
	runtimeRestoreProject = flag.String("runtime-restore-project", "", "private project for the restore test subprocess")
	runtimeRestorePhase   = flag.Int("runtime-restore-phase", 0, "restore test subprocess stage")
)

const restoreBashInput = `{"command":"printf 'executed\\n' >> approval-marker.txt"}`

type restoredDecision struct {
	Code int                          `json:"code"`
	Data dtochat.ApprovalDecisionResp `json:"data"`
}

// 新进程运行真实appRuntime，隔离Shutdown/secret/Default等包级状态。
// 模型只访问父进程mock；真实Bash仅在主目录内隔离项目追加marker。
func TestRuntimeRestoredApprovalsDoNotReplay(t *testing.T) {
	if *runtimeRestoreFixture != "" {
		runRestoredRuntime(t)
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	var modelCalls, titleCalls, mcpCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			mcpCalls.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var request struct {
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error("invalid mock request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(request.Tools) == 0 {
			titleCalls.Add(1)
			// 标题使用Generate的非流JSON，聊天使用Stream的SSE。
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "restore-title", "object": "chat.completion", "model": "fixture", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "恢复验收"}, "finish_reason": "stop"}}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(delta any, reason any) {
			chunk := map[string]any{"id": "restore", "object": "chat.completion.chunk", "model": "fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		if len(request.Tools) > 0 && modelCalls.Add(1)%2 == 1 {
			emit(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "reused-call", "type": "function", "function": map[string]any{"name": "bash", "arguments": restoreBashInput}}}}, nil)
			emit(map[string]any{}, "tool_calls")
		} else {
			emit(map[string]any{"role": "assistant", "content": "恢复验收"}, nil)
			emit(map[string]any{}, "stop")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{"pre-eight", "pre-hardening"} {
		for _, persistWAL := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/retained_sidecars_%t", schema, persistWAL), func(t *testing.T) {
				project, err := os.MkdirTemp(home, ".kaguya-runtime-restore-")
				if err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(project)
				original := t.TempDir()
				seedRuntimeRestore(t, original, project, schema, server.URL, persistWAL)
				backup, digests := copyRuntimeRestore(t, original)
				wantFiles := 3 // DB/key/schema; only existing sidecars are copied.
				if persistWAL {
					wantFiles = 5
				}
				if len(digests) != wantFiles {
					t.Fatalf("backup files=%d, want %d", len(digests), wantFiles)
				}
				for _, suffix := range []string{"-wal", "-shm"} {
					if persistWAL {
						info, err := os.Stat(filepath.Join(backup, "restored.db"+suffix))
						if err != nil || info.Size() == 0 {
							t.Fatal("retained sidecar is missing or empty")
						}
					}
				}
				dir, _ := copyRuntimeRestore(t, backup)
				for phase := 1; phase <= 2; phase++ {
					before := modelCalls.Load()
					beforeTitles := titleCalls.Load()
					ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
					command := exec.CommandContext(ctx, executable, "-test.run=^TestRuntimeRestoredApprovalsDoNotReplay$", "-test.v", "-test.timeout=30s", "-runtime-restore-fixture="+dir, "-runtime-restore-project="+project, fmt.Sprintf("-runtime-restore-phase=%d", phase))
					for _, value := range os.Environ() {
						// 测试排除shell启动hook，不改变产品的Bash环境合同。
						if !strings.HasPrefix(value, "BASH_ENV=") && !strings.HasPrefix(value, "ENV=") && !strings.HasPrefix(value, "BASH_FUNC_") {
							command.Env = append(command.Env, value)
						}
					}
					output, err := command.CombinedOutput()
					cancel()
					if err != nil {
						t.Fatalf("restore phase %d: %v\n%s", phase, err, output)
					}
					if phase == 2 && (modelCalls.Load() != before || titleCalls.Load() != beforeTitles) {
						t.Fatal("second startup replayed a model/tool call")
					}
					for name, digest := range digests {
						data, err := os.ReadFile(filepath.Join(backup, name))
						if err != nil || sha256.Sum256(data) != digest {
							t.Fatal("full runtime modified the original offline backup")
						}
					}
					t.Logf("RUNTIME_RESTORE schema=%s retained_sidecars=%t backup_files=%d phase=%d subprocess=PASS chat_model_calls=%d title_model_calls=%d mcp_requests=%d", schema, persistWAL, len(digests), phase, modelCalls.Load()-before, titleCalls.Load()-beforeTitles, mcpCalls.Load())
				}
				if schema == "pre-hardening" {
					assertRestoreMarker(t, project, true)
				} else {
					assertRestoreMarker(t, project, false)
				}
			})
		}
	}
	if modelCalls.Load() != 8 || titleCalls.Load() != 2 || mcpCalls.Load() != 0 {
		t.Fatalf("unexpected calls: chat=%d title=%d mcp=%d", modelCalls.Load(), titleCalls.Load(), mcpCalls.Load())
	}
}

// seed函数已关闭原库全部连接；复制实际存在的文件而不打开备份写入。
func copyRuntimeRestore(t *testing.T, source string) (string, map[string][32]byte) {
	t.Helper()
	dir := t.TempDir()
	digests := map[string][32]byte{}
	for _, name := range []string{"restored.db", "restored.db-wal", "restored.db-shm", "key", "schema"} {
		data, err := os.ReadFile(filepath.Join(source, name))
		if os.IsNotExist(err) && (strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm")) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		digests[name] = sha256.Sum256(data)
	}
	return dir, digests
}

func seedRuntimeRestore(t *testing.T, dir, project, schema, url string, persistWAL bool) {
	t.Helper()
	cfg := config.DatabaseConfig{Path: filepath.Join(dir, "restored.db"), KeyFile: filepath.Join(dir, "key")}
	if err := os.WriteFile(cfg.KeyFile, []byte(strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureSQLiteDatabase(); err != nil {
		t.Fatal(err)
	}
	dsn, err := cfg.SQLiteDSN()
	if err != nil {
		t.Fatal(err)
	}
	pool, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal("open fixture failed")
	}
	defer pool.Close()
	pool.SetMaxOpenConns(1)
	ddl, err := os.ReadFile(filepath.Join("..", "db", "testdata", "legacy-"+schema+".sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	fts, err := os.ReadFile(filepath.Join("..", "db", "testdata", "legacy-memory-fts.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(string(fts)); err != nil {
		t.Fatal(err)
	}
	key, err := cfg.SQLCipherKeyBytes()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.NewCipher("", key)
	if err != nil {
		t.Fatal(err)
	}
	providerKey, err := cipher.Encrypt("runtime-fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	const stamp = "2026-01-01 00:00:00"
	insert := func(table, columns string, args ...any) {
		query := "INSERT INTO " + table + " (id,created_at,updated_at," + columns + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", len(args)+2), ",") + ")"
		values := append([]any{args[0], stamp, stamp}, args[1:]...)
		if _, err := pool.Exec(query, values...); err != nil {
			t.Fatalf("seed table %s: %v", table, err)
		}
	}
	insert("kaguya_provider_info", "provider_name,api_key,base_url", "runtime-provider", "隔离提供商", providerKey, url)
	insert("kaguya_models_info", "model_name,model_id,api_protocol,provider_id,reasoning_enabled,token_context_window", "runtime-model", "隔离模型", "fixture", "openai-chat", "runtime-provider", 0, 8192)
	insert("kaguya_system_info", "default_model_id,task_model_id,model_sync_enabled", consts.SystemInfoID, "runtime-model", "runtime-model", false)
	insert("kaguya_project", "name,path", "runtime-project", "隔离恢复项目", project)
	columns := "title,project_id,last_message_at,model_id,model_name,turn_count"
	args := []any{"runtime-conv", "旧会话", "runtime-project", stamp, "fixture", "隔离模型", 2}
	if schema == "pre-hardening" {
		columns += ",permission_mode"
		args = append(args, "full_approval")
	}
	insert("kaguya_conversation", columns, args...)
	for i, status := range []string{"running", "completed"} {
		insert("kaguya_chat_turn", "conversation_id,turn_index,user_content,provider_id,provider_name,model_id,model_name,api_protocol,status,started_at,finished_at,duration_ms,tool_calls,finish_reason,input_tokens,output_tokens,total_tokens,cached_tokens,reasoning_tokens,messages", fmt.Sprintf("runtime-turn-%d", i), "runtime-conv", i+1, "旧问题", "runtime-provider", "隔离提供商", "fixture", "隔离模型", "openai-chat", status, stamp, stamp, 1, 1, "stop", 1, 1, 2, 0, 0, "[ ]")
	}
	if schema == "pre-hardening" {
		for i, status := range []string{"pending", "approved", "consumed"} {
			decision, callID := "", fmt.Sprintf("old-call-%d", i)
			if i > 0 {
				decision = "approve"
			}
			if i == 2 {
				callID = "reused-call"
			}
			insert("kaguya_approval", "approval_id,conversation_id,turn_id,tool_call_id,input_hash,tool_identity,effect,mode,status,decision,requested_at", fmt.Sprintf("runtime-old-row-%d", i), fmt.Sprintf("runtime-old-%d", i), "runtime-conv", "runtime-turn-0", callID, permission.HashInput(restoreBashInput), "builtin:bash", "unknown", "full_approval", status, decision, stamp)
		}
		insert("kaguya_mcp_server", "name,transport,args,env,headers,url,enabled,reconnect_failures,reconnect_circuit_open,reconnect_cycle", "runtime-mcp", "开路服务", "streamable-http", "[]", "{}", "{}", url+"/mcp", true, 5, true, 4)
		insert("kaguya_conversation", "title,last_message_at,model_id,model_name", "runtime-title-conv", "新对话", stamp, "fixture", "隔离模型")
		insert("kaguya_title_job", "conversation_id,enqueue_order,status,question,answer,revision,attempt_count,started_at", "runtime-title-job", "runtime-title-conv", 1, "running", "恢复前问题", "恢复前回答", 9, 2, stamp)
	}
	if persistWAL {
		conn, err := pool.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		err = conn.Raw(func(driver any) error {
			native, ok := driver.(*sqlite3.SQLiteConn)
			if !ok {
				return fmt.Errorf("fixture is not native SQLite")
			}
			return native.SetFileControlInt("main", sqlite3.SQLITE_FCNTL_PERSIST_WAL, 1)
		})
		_ = conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		// Only the fixture retains sidecars; final close may checkpoint their data.
		if _, err := pool.Exec("PRAGMA user_version=43"); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "schema"), []byte(schema), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runRestoredRuntime(t *testing.T) {
	dir, project := *runtimeRestoreFixture, *runtimeRestoreProject
	global.Cfg = config.Cli{Web: true, Host: "127.0.0.1", Port: 9024, MachineID: 1, RouterPrefix: "/kaguya/api", DB: config.DatabaseConfig{Path: filepath.Join(dir, "restored.db"), KeyFile: filepath.Join(dir, "key")}, LogInfo: config.LogConfig{Module: "restore-test", Level: config.InfoLevel, Format: config.JSONFormat}}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	rt := newAppRuntime(ctx)
	defer rt.close()
	if err := rt.init(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-rt.restoreDone:
	case <-ctx.Done():
		t.Fatal("MCP restore did not drain")
	}
	turn, err := db.EntClient.KaguyaChatTurn.Get(ctx, "runtime-turn-0")
	if err != nil || string(turn.Status) != "interrupted" {
		t.Fatal("runtime did not reconcile old turn")
	}
	schema, err := os.ReadFile(filepath.Join(dir, "schema"))
	if err != nil {
		t.Fatal(err)
	}
	if string(schema) == "pre-hardening" {
		for i, state := range []string{"canceled", "canceled", "consumed"} {
			row, err := db.EntClient.KaguyaApproval.Get(ctx, fmt.Sprintf("runtime-old-row-%d", i))
			if err != nil || string(row.Status) != state || row.ExecutionFinishedAt != nil {
				t.Fatal("runtime changed old execution/authorization incorrectly")
			}
		}
		title, err := (&serviceagent.AgentSvc{}).ConversationTitleWait(ctx, "runtime-title-conv")
		if err != nil || title.Status != "succeeded" || title.Title != "恢复验收" {
			t.Fatal("restored durable title job did not complete through the real worker")
		}
		job, err := db.EntClient.KaguyaTitleJob.Get(ctx, "runtime-title-job")
		if err != nil || job.Question != "恢复前问题" || job.Answer != "恢复前回答" || job.Revision != 12 || job.AttemptCount != 3 {
			t.Fatal("full runtime lost persisted title source or repeated its claim")
		}
		if *runtimeRestorePhase == 1 {
			handler, err := rt.newEngine(gin.New())
			if err != nil {
				t.Fatal(err)
			}
			for round := 0; round < 2; round++ {
				frames := make(chan *dtochat.ChatResp, 64)
				go (&serviceagent.AgentSvc{}).Chat(rt.RootContext(), frames, &dtochat.ChatReq{ID: "runtime-conv", ModelID: "runtime-model", Messages: "恢复后的新调用"})
				fresh, completed := "", false
				for frame := range frames {
					if frame.Chat.Flag == dtochat.ChatFlagError {
						t.Fatal("restored chat emitted error")
					}
					if frame.Approval != nil && frame.Approval.Status == "pending" {
						if fresh != "" || strings.HasPrefix(frame.Approval.ApprovalID, "runtime-old-") || frame.Approval.ToolCallID != "reused-call" {
							t.Fatal("new call reused restored authorization")
						}
						fresh = frame.Approval.ApprovalID
						assertRestoreMarker(t, project, false)
						for _, id := range []string{"runtime-old-0", "runtime-old-1"} {
							if response := runtimeApprovalAPI(t, handler, id, "approve"); response.Code == dtocode.SystemSuccess.Code {
								t.Fatal("startup-canceled approval API granted a new decision")
							}
						}
						old := runtimeApprovalAPI(t, handler, "runtime-old-2", "approve")
						if old.Code != dtocode.SystemSuccess.Code || old.Data.Status != "consumed" {
							t.Fatal("old decision did not return persisted audit state")
						}
						assertRestoreMarker(t, project, false)
						if pending, err := (&serviceagent.AgentSvc{}).ConversationApprovals(ctx, "runtime-conv"); err != nil || len(pending) != 1 || pending[0].ApprovalID != fresh {
							t.Fatal("old decision resolved new waiter")
						}
						decision := "reject"
						if round == 1 {
							decision = "approve"
						}
						for retry := 0; retry < 2; retry++ {
							if response := runtimeApprovalAPI(t, handler, fresh, decision); response.Code != dtocode.SystemSuccess.Code {
								t.Fatal("fresh decision API failed")
							}
						}
					}
					completed = completed || frame.Chat.Flag == dtochat.ChatFlagDone
				}
				if fresh == "" || !completed {
					t.Fatal("restored chat did not complete with fresh approval")
				}
				assertRestoreMarker(t, project, round == 1)
				row, err := db.EntClient.KaguyaApproval.Query().Where(kaguyaapproval.ApprovalIDEQ(fresh)).Only(ctx)
				if err != nil || (round == 0 && (row.Status != "rejected" || row.ExecutionFinishedAt != nil)) || (round == 1 && (row.Status != "consumed" || row.ExecutionState != "succeeded" || row.ExecutionFinishedAt == nil)) {
					t.Fatal("fresh approval audit does not match actual tool execution")
				}
			}
		}
		pending, err := (&serviceagent.AgentSvc{}).ConversationApprovals(ctx, "runtime-conv")
		if err != nil || len(pending) != 0 {
			t.Fatal("restored process retained waiters")
		}
	}
	rt.beginShutdown()
	if err := serviceagent.WaitActive(ctx); err != nil {
		t.Fatal(err)
	}
	rt.close()
	for name, done := range map[string]<-chan struct{}{"instructions": rt.instructions.Done(), "titles": rt.titleQueue.Done(), "restore": rt.restoreDone, "recovery": rt.recoveryDone, "catalog": rt.modelSyncDone} {
		select {
		case <-done:
		default:
			t.Fatalf("owner %s did not stop", name)
		}
	}
	if _, err := db.EntClient.KaguyaConversation.Query().Count(context.Background()); err == nil {
		t.Fatal("database remained open after shutdown")
	}
}

func runtimeApprovalAPI(t *testing.T, handler http.Handler, id, decision string) restoredDecision {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9024/kaguya/api/v1/chat/conversation/runtime-conv/approvals/"+id, strings.NewReader(`{"decision":"`+decision+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://127.0.0.1:9024")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var result restoredDecision
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatal("approval API did not return JSON envelope")
	}
	return result
}

func assertRestoreMarker(t *testing.T, project string, executed bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(project, "approval-marker.txt"))
	if !executed {
		if !os.IsNotExist(err) {
			t.Fatal("Bash executed without fresh approved consumption")
		}
		return
	}
	if err != nil || string(data) != "executed\n" {
		t.Fatal("fresh approval did not execute Bash exactly once")
	}
}
