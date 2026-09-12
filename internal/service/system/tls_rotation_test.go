package system

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dto "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/internal/secret"
	initialize "github.com/lyonmu/kaguya/internal/init"
	"go.uber.org/zap"
)

// errInjectedFailure 模拟轮换写库失败。
var errInjectedFailure = errors.New("injected database failure")

// faultPattern 描述一次按出现次数触发的语句故障。
type faultPattern struct {
	substr string
	nth    int
	seen   int
}

// faultDriver 包装真实 SQLite 驱动，按语句内容注入一次失败。
type faultDriver struct {
	base     driver.Driver
	mu       sync.Mutex
	patterns []*faultPattern
}

func (d *faultDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &faultConn{conn: conn, owner: d}, nil
}

// failOnNth 让包含 substr 的第 n 次匹配返回注入错误。
func (d *faultDriver) failOnNth(substr string, nth int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.patterns = append(d.patterns, &faultPattern{substr: substr, nth: nth})
}

func (d *faultDriver) inject(query string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, p := range d.patterns {
		if !strings.Contains(query, p.substr) {
			continue
		}
		p.seen++
		if p.seen != p.nth {
			continue
		}
		d.patterns = append(d.patterns[:i], d.patterns[i+1:]...)
		return errInjectedFailure
	}
	return nil
}

type faultConn struct {
	conn  driver.Conn
	owner *faultDriver
}

func (c *faultConn) Prepare(query string) (driver.Stmt, error) {
	if err := c.owner.inject(query); err != nil {
		return nil, err
	}
	stmt, err := c.conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &faultStmt{stmt: stmt}, nil
}

func (c *faultConn) Close() error { return c.conn.Close() }

func (c *faultConn) Begin() (driver.Tx, error) { //nolint:staticcheck // 底层驱动只实现旧接口
	tx, err := c.conn.Begin() //nolint:staticcheck // 底层驱动只实现旧接口
	if err != nil {
		return nil, err
	}
	return &faultTx{tx: tx}, nil
}

type faultStmt struct{ stmt driver.Stmt }

func (s *faultStmt) Close() error  { return s.stmt.Close() }
func (s *faultStmt) NumInput() int { return s.stmt.NumInput() }

func (s *faultStmt) Exec(args []driver.Value) (driver.Result, error) { //nolint:staticcheck // 底层驱动只实现旧接口
	return s.stmt.Exec(args) //nolint:staticcheck // 底层驱动只实现旧接口
}

func (s *faultStmt) Query(args []driver.Value) (driver.Rows, error) { //nolint:staticcheck // 底层驱动只实现旧接口
	return s.stmt.Query(args) //nolint:staticcheck // 底层驱动只实现旧接口
}

type faultTx struct{ tx driver.Tx }

func (t *faultTx) Commit() error   { return t.tx.Commit() }
func (t *faultTx) Rollback() error { return t.tx.Rollback() }

var faultDriverSeq int

// setupFaultSystemTest 使用文件数据库，便于模拟“轮换失败后重启”的场景。
func setupFaultSystemTest(t *testing.T) (context.Context, *faultDriver) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	dbFile, err := os.CreateTemp(t.TempDir(), "tls-rotate-*.db")
	if err != nil {
		t.Fatal(err)
	}
	_ = dbFile.Close()
	dsn := dbFile.Name()

	base, err := sql.Open(dialect.SQLite, dsn)
	if err != nil {
		t.Fatal(err)
	}
	fault := &faultDriver{base: base.Driver()}
	faultDriverSeq++
	name := fmt.Sprintf("kaguya-system-fault-%d", faultDriverSeq)
	sql.Register(name, fault)
	conn, err := sql.Open(name, dsn+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, conn)))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldID, oldLogger := db.EntClient, global.Id, global.Logger
	gen := &testIDGenerator{}
	db.EntClient, global.Id, global.Logger = client, gen, zap.NewNop()
	t.Cleanup(func() { db.EntClient, global.Id, global.Logger = oldClient, oldID, oldLogger })
	if err := initialize.Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	return ctx, fault
}

// TLS 轮换中途写入失败时必须整体回滚：旧证书、旧密文与旧活动密钥保持一致，
// 重启后用旧证书派生密钥仍能读出全部 API Key。
func TestTLSRotationFailureKeepsOldSecrets(t *testing.T) {
	ctx, fault := setupFaultSystemTest(t)
	svc := &SystemSvc{}
	if _, err := svc.PrepareTLS(ctx, []string{"agent.example.com"}); err != nil {
		t.Fatal(err)
	}
	// 用证书私钥派生密钥，触发轮换重加密路径。
	if err := svc.InitSecret(ctx, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secret.Reset)

	first, err := svc.ProviderCreate(ctx, providerSaveReq("first", "sk-first-secret-value"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.ProviderCreate(ctx, providerSaveReq("second", "sk-second-secret-value"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil {
		t.Fatal(err)
	}
	beforeKeys := map[string]string{}
	for _, id := range []string{first.ID, second.ID} {
		row, err := db.EntClient.KaguyaProviderInfo.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		beforeKeys[id] = row.APIKey
	}

	// 第一条密文重写成功，第二条失败：整个事务必须回滚。
	fault.failOnNth("UPDATE `kaguya_provider_info`", 2)
	if _, err := svc.TLSUpdate(ctx, &dto.TLSSaveReq{Generate: true, Hosts: []string{"rotated.example.com"}}); err == nil {
		t.Fatal("rotation must fail when a provider key write fails")
	}

	after, err := db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil {
		t.Fatal(err)
	}
	if after.TLSCertificatePem != before.TLSCertificatePem || after.TLSPrivateKeyPem != before.TLSPrivateKeyPem {
		t.Fatal("failed rotation changed the stored certificate")
	}
	for id, want := range beforeKeys {
		row, err := db.EntClient.KaguyaProviderInfo.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if row.APIKey != want {
			t.Fatalf("failed rotation left a partially rewritten key: id=%s", id)
		}
	}

	// 模拟重启：重新用旧证书派生密钥，全部明文必须仍可读出。
	secret.Reset()
	if err := svc.InitSecret(ctx, ""); err != nil {
		t.Fatalf("init after failed rotation: %v", err)
	}
	for id, want := range map[string]string{first.ID: "sk-first-secret-value", second.ID: "sk-second-secret-value"} {
		plain, err := svc.ProviderAPIKey(ctx, id)
		if err != nil {
			t.Fatalf("read key after failed rotation: %v", err)
		}
		if plain.APIKey != want {
			t.Fatalf("key mismatch after restart: id=%s got=%q want=%q", id, plain.APIKey, want)
		}
	}
}

// 无效的显式密钥不得清空已经可用的活动密钥。
func TestInitSecretKeepsActiveCipherOnInvalidExplicitKey(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}
	if _, err := svc.PrepareTLS(ctx, []string{"agent.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.InitSecret(ctx, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secret.Reset)
	provider, err := svc.ProviderCreate(ctx, providerSaveReq("keep", "sk-keep-me-please"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.InitSecret(ctx, "not-a-valid-key"); err == nil {
		t.Fatal("invalid explicit key must fail")
	}
	if !secret.Enabled() {
		t.Fatal("failed init must keep the previous active key")
	}
	plain, err := svc.ProviderAPIKey(ctx, provider.ID)
	if err != nil || plain.APIKey != "sk-keep-me-please" {
		t.Fatalf("key lost after failed init: %+v err=%v", plain, err)
	}
}
