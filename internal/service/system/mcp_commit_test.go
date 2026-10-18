package system

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
)

// 在真实 SQLCipher Commit 边界注入错误，保留 driver 的上下文、statement
// 与事务能力，不把 hook 的“SQL 尚未执行”当作 COMMIT 不确定性的证明。
type mcpCommitDriver struct {
	dialect.Driver
	mode   atomic.Int32
	faults atomic.Int64
}

func (d *mcpCommitDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	tx, err := d.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return &mcpCommitTx{Tx: tx, owner: d}, nil
}

type mcpCommitTx struct {
	dialect.Tx
	owner *mcpCommitDriver
}

func (tx *mcpCommitTx) Commit() error {
	switch tx.owner.mode.Swap(0) {
	case 1:
		tx.owner.faults.Add(1)
		_ = tx.Tx.Rollback()
		return mcpTestStorageCause
	case 2:
		if err := tx.Tx.Commit(); err != nil {
			return err
		}
		tx.owner.faults.Add(1)
		return mcpTestStorageCause
	default:
		return tx.Tx.Commit()
	}
}

func setupMCPCommitFault(t *testing.T) (context.Context, *mcpCommitDriver, *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	dir := t.TempDir()
	cfg := config.DatabaseConfig{Path: filepath.Join(dir, "mcp.db"), KeyFile: filepath.Join(dir, "key")}
	if err := os.WriteFile(cfg.KeyFile, []byte(strings.Repeat("ab", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	dsn, err := cfg.SQLiteDSN()
	if err != nil {
		t.Fatal(err)
	}
	pool, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	fault := &mcpCommitDriver{Driver: entsql.OpenDB(dialect.SQLite, pool)}
	client := ent.NewClient(ent.Driver(fault))
	var cipherVersion string
	if err := pool.QueryRowContext(ctx, "PRAGMA cipher_version").Scan(&cipherVersion); err != nil || cipherVersion == "" {
		t.Fatal("fixture requires SQLCipher", err)
	}
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldID, oldLogger, oldPolicy := db.EntClient, global.Id, global.Logger, DefaultMCPRecovery
	db.EntClient, global.Id, global.Logger, DefaultMCPRecovery = client, &testIDGenerator{}, zap.NewNop(), NewMCPRecoveryCoordinator()
	t.Cleanup(func() {
		_ = client.Close()
		db.EntClient, global.Id, global.Logger, DefaultMCPRecovery = oldClient, oldID, oldLogger, oldPolicy
	})
	return ctx, fault, pool
}

func TestMCPPolicyCommitUncertaintyRequiresExplicitReconciliation(t *testing.T) {
	for _, kind := range []string{"success", "failure", "manual", "clear"} {
		t.Run(kind, func(t *testing.T) {
			for _, mode := range []int32{1, 2} {
				t.Run(map[int32]string{1: "before_commit", 2: "after_commit"}[mode], func(t *testing.T) {
					ctx, fault, pool := setupMCPCommitFault(t)
					row := mcpPolicyRow(t, ctx)
					confirmed, _ := DefaultMCPRecovery.Snapshot(row.ID)
					fault.mode.Store(mode)
					var err error
					switch kind {
					case "success":
						err = DefaultMCPRecovery.RecordSuccess(ctx, row.ID)
					case "failure":
						_, err = DefaultMCPRecovery.RecordFailure(ctx, row.ID, time.Millisecond, time.Second, time.Now())
					case "manual":
						_, err = DefaultMCPRecovery.StartManualCycle(ctx, row.ID)
					case "clear":
						err = DefaultMCPRecovery.Clear(ctx, row.ID)
					}
					if err == nil {
						t.Fatal("fault did not reach coordinator")
					}
					state, _ := DefaultMCPRecovery.Snapshot(row.ID)
					expected := confirmed
					expected.PersistFail = true
					if state != expected || fault.faults.Load() != 1 {
						t.Fatalf("unconfirmed policy published: %+v faults=%d", state, fault.faults.Load())
					}
					stored, err := db.EntClient.KaguyaMCPServer.Get(ctx, row.ID)
					if err != nil {
						t.Fatal(err)
					}
					durableCycle, durableFailures := confirmed.Cycle, confirmed.Failures
					if mode == 2 {
						durableFailures = 0
						if kind == "manual" {
							durableCycle++
						}
						if kind == "failure" {
							durableFailures = 5
						}
					}
					if stored.ReconnectCycle != durableCycle || stored.ReconnectFailures != durableFailures {
						t.Fatalf("fixture did not exercise actual COMMIT: %+v", stored)
					}
					for range 3 {
						if DefaultMCPRecovery.RecordSuccess(ctx, row.ID) == nil || DefaultMCPRecovery.Allowed(row.ID, time.Now().Add(time.Hour)) {
							t.Fatal("healthy inferred success from uncertain write")
						}
					}
					// 明确的新周期重新读真实持久行，不能用故障前内存值永久卡住 CAS。
					next, err := DefaultMCPRecovery.StartManualCycle(ctx, row.ID)
					if err != nil || next.Cycle != durableCycle+1 || next.PersistFail || next.Failures != 0 {
						t.Fatalf("explicit reconciliation failed: %+v %v", next, err)
					}
					assertMCPPolicy(t, row.ID, next)
					if pool.Stats().InUse != 0 {
						t.Fatal("commit fault retained physical transaction")
					}
				})
			}
		})
	}
}
