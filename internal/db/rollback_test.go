package db_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/consts"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
)

var (
	rollbackBinaryA = flag.String("rollback-binary-a", "", "production binary built from the pre-eight fixture commit")
	rollbackBinaryB = flag.String("rollback-binary-b", "", "production binary built from the pre-hardening fixture commit")
)

// 显式传入匹配的真实main二进制；默认回归不冒充历史版本回滚已执行。
// 旧版只读写升级前备份的副本；升级库和唯一原备份均不交给旧版。
func TestHistoricalBinaryRollback(t *testing.T) {
	if *rollbackBinaryA == "" && *rollbackBinaryB == "" {
		t.Skip("pass both historical production binaries to run the rollback gate")
	}
	if *rollbackBinaryA == "" || *rollbackBinaryB == "" {
		t.Fatal("both historical binaries are required")
	}
	for _, version := range []struct {
		name, commit, binary string
		approval             bool
	}{
		{"pre-eight", "49d5f878ebfe0bc9b1a7313cbf19b9623697d73a", *rollbackBinaryA, false},
		{"pre-hardening", "7db2f863a5953a610c27cffab7a40140462ee151", *rollbackBinaryB, true},
	} {
		binary, err := filepath.Abs(version.binary)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		output, err := exec.CommandContext(ctx, binary, "--version").CombinedOutput()
		cancel()
		if err != nil || !strings.Contains(string(output), version.commit) || !strings.Contains(string(output), "production") {
			t.Fatal("historical binary version does not match its schema commit/production tag")
		}
		binaryHash := sha256.Sum256(data)
		for _, persistWAL := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/persist_wal_%t", version.name, persistWAL), func(t *testing.T) {
				var transportCalls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					transportCalls.Add(1)
					w.WriteHeader(http.StatusServiceUnavailable)
				}))
				defer server.Close()
				cfg := restoreConfig(t)
				pool := restoreOpen(t, cfg)
				for _, name := range []string{"legacy-" + version.name + ".sql", "legacy-memory-fts.sql"} {
					ddl, err := restoreSchemas.ReadFile("testdata/" + name)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := pool.Exec(string(ddl)); err != nil {
						t.Fatal(err)
					}
				}
				restoreSeed(t, pool, cfg, version.approval, server.URL)
				// 隔离版本恢复不请求外部目录/模型；标题任务按旧合同明确失败。
				restoreInsert(t, pool, "kaguya_system_info", map[string]any{"id": consts.SystemInfoID, "created_at": "2026-01-01 00:00:00", "updated_at": "2026-01-01 00:00:00", "model_sync_enabled": false, "model_sync_url": consts.DefaultModelCatalogURL, "provider_sync_url": consts.DefaultProviderCatalogURL, "global_agents_paths": "[]"})
				if _, err := pool.Exec("UPDATE kaguya_provider_info SET base_url = ? WHERE id = 'provider-1'", server.URL); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec("UPDATE kaguya_models_info SET request_path = '/chat/completions' WHERE id = 'model-local'"); err != nil {
					t.Fatal(err)
				}
				original := restoreCapture(t, pool)
				restoreClose(t, pool, persistWAL)
				backup := restoreCopy(t, cfg, persistWAL)
				// 同一旧备份先证明当前版本两次升级和升级后备份可恢复。
				upgrade := restoreCopy(t, backup.cfg, persistWAL)
				for attempt := 0; attempt < 2; attempt++ {
					restoreMigrate(t, upgrade.cfg, version.approval)
					restoreCompare(t, upgrade.cfg, original)
				}
				pool = restoreOpen(t, upgrade.cfg)
				restoreClose(t, pool, persistWAL)
				upgradedBackup := restoreCopy(t, upgrade.cfg, persistWAL)
				rollback := restoreCopy(t, backup.cfg, persistWAL)
				var first map[string]restoreSnapshot
				for attempt := 0; attempt < 2; attempt++ {
					runHistoricalApplication(t, binary, rollback.cfg, version.approval)
					pool = restoreOpen(t, rollback.cfg)
					current := restoreCapture(t, pool)
					assertHistoricalRecovery(t, original, current, version.approval)
					var integrity string
					if err := pool.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
						t.Fatal("historical runtime left invalid database")
					}
					restoreClose(t, pool, false)
					if attempt == 0 {
						first = current
					} else if !reflect.DeepEqual(first, current) {
						t.Fatal("second historical startup changed terminal recovered data")
					}
				}
				if transportCalls.Load() != 0 {
					t.Fatal("rollback made an external model/MCP attempt")
				}
				restoreBackupUnchanged(t, backup)
				restoreBackupUnchanged(t, upgradedBackup)
				t.Logf("ROLLBACK schema=%s commit=%s binary_sha256=%x retained_sidecars=%t old_files=%d upgraded_files=%d starts=2 http_history=PASS old_columns=PASS second_start=unchanged transport_requests=0", version.name, version.commit, binaryHash, persistWAL, len(backup.files), len(upgradedBackup.files))
			})
		}
	}
}

func runHistoricalApplication(t *testing.T, binary string, cfg *config.DatabaseConfig, approval bool) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "--web", "--host=127.0.0.1", "--port="+strconv.Itoa(port), "--db.path="+cfg.Path, "--db.key-file="+cfg.KeyFile, "--log.file-enabled=false", "--log.console-enabled=true", "--log.format=json")
	command.Dir = filepath.Dir(cfg.Path)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "KAGUYA_SECRET_KEY=") && !strings.HasPrefix(value, "BASH_ENV=") && !strings.HasPrefix(value, "ENV=") && !strings.HasPrefix(value, "BASH_FUNC_") {
			command.Env = append(command.Env, value)
		}
	}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	joined := false
	defer func() {
		if !joined {
			_ = command.Process.Kill()
			<-done
		}
	}()
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/kaguya/api/v1/chat/conversation/conv-0/turns?limit=10"
	client := &http.Client{Timeout: time.Second}
	ready := false
	for !ready {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err == nil {
			var envelope struct {
				Code int `json:"code"`
				Data struct {
					Items []struct {
						ID          string `json:"id"`
						UserContent string `json:"user_content"`
						Status      string `json:"status"`
					} `json:"items"`
				} `json:"data"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&envelope)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && envelope.Code == dtocode.SystemSuccess.Code {
				if len(envelope.Data.Items) != 5 {
					t.Fatal("historical HTTP history lost turns")
				}
				want := map[string]string{"turn-0": "interrupted", "turn-1": "completed", "turn-2": "interrupted", "turn-3": "failed", "turn-4": "canceled"}
				for _, item := range envelope.Data.Items {
					if item.UserContent != "旧提问\n'特殊字符\\" || item.Status != want[item.ID] || want[item.ID] == "" {
						t.Fatal("historical HTTP history lost turn identity/input/status")
					}
					delete(want, item.ID)
				}
				if len(want) != 0 {
					t.Fatal("historical HTTP history returned duplicate/missing turns")
				}
				ready = true
			} else {
				t.Fatalf("historical HTTP history failed: status=%d code=%d decode=%v", response.StatusCode, envelope.Code, decodeErr)
			}
		}
		if !ready {
			select {
			case err := <-done:
				joined = true
				t.Fatalf("historical process exited before readiness: %v\n%s", err, output.Bytes())
			case <-ctx.Done():
				t.Fatal("historical HTTP startup timed out")
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	if approval {
		// 旧版缺任务模型时明确终结两个未完成标题，不发模型请求。
		pool := restoreOpen(t, cfg)
		for {
			var terminal int
			if err := pool.QueryRow("SELECT count(*) FROM kaguya_title_job WHERE id IN ('job-0','job-1') AND status='failed' AND error_code='task_model_not_configured'").Scan(&terminal); err != nil {
				t.Fatal(err)
			}
			if terminal == 2 {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("historical title recovery did not terminate")
			case <-time.After(20 * time.Millisecond):
			}
		}
		if err := pool.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		joined = true
		if err != nil {
			t.Fatalf("historical graceful shutdown failed: %v\n%s", err, output.Bytes())
		}
	case <-ctx.Done():
		t.Fatal("historical graceful shutdown timed out")
	}
	for _, forbidden := range []string{"fixture-provider-secret", "fake-env", "fake-header", strings.Repeat("ab", 32), "enc:v2:", "timed out", "storage_error"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatal("historical process leaked a secret or failed resource drain")
		}
	}
	// 退出进程后监听必须消失，再由调用方重新打开SQLCipher检查原列。
	if connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second); err == nil {
		_ = connection.Close()
		t.Fatal("historical listener survived shutdown")
	}
}

func assertHistoricalRecovery(t *testing.T, before, after map[string]restoreSnapshot, approval bool) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatal("historical runtime added/removed an ordinary table")
	}
	for table, snapshot := range before {
		current, ok := after[table]
		if !ok || !reflect.DeepEqual(snapshot.columns, current.columns) || len(snapshot.rows) != len(current.rows) {
			t.Fatalf("historical runtime changed structure/row count of %s", table)
		}
		for i, row := range snapshot.rows {
			id, _ := row[0].(string)
			value := func(name string) any {
				for index, column := range current.columns {
					if column == name {
						return current.rows[i][index]
					}
				}
				t.Fatalf("historical field %s.%s is absent", table, name)
				return nil
			}
			switch {
			case table == "kaguya_chat_turn" && id == "turn-0":
				if value("status") != "interrupted" {
					t.Fatal("historical runtime did not reconcile the interrupted turn")
				}
			case approval && table == "kaguya_approval" && (id == "approval-row-0" || id == "approval-row-1"):
				if value("status") != "canceled" || value("reason_code") != "startup_recovery" || value("resolved_at") == nil {
					t.Fatal("historical runtime retained old approval authorization")
				}
			case approval && table == "kaguya_title_job" && (id == "job-0" || id == "job-1"):
				if value("status") != "failed" || value("error_code") != "task_model_not_configured" || value("attempt_count") != int64(4) || value("finished_at") == nil {
					t.Fatal("historical title recovery did not follow the old missing-model contract")
				}
			}
			for columnIndex, name := range snapshot.columns {
				allowed := table == "kaguya_chat_turn" && id == "turn-0" && (name == "status" || name == "finished_at" || name == "duration_ms" || name == "updated_at")
				allowed = allowed || approval && table == "kaguya_approval" && (id == "approval-row-0" || id == "approval-row-1") && (name == "status" || name == "reason_code" || name == "resolved_at" || name == "updated_at")
				allowed = allowed || approval && table == "kaguya_title_job" && (id == "job-0" || id == "job-1") && (name == "status" || name == "attempt_count" || name == "started_at" || name == "finished_at" || name == "error_code" || name == "updated_at")
				if !allowed && !reflect.DeepEqual(row[columnIndex], current.rows[i][columnIndex]) {
					t.Fatalf("historical startup changed preserved column %s.%s", table, name)
				}
			}
		}
	}
}
