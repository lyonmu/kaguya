package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lyonmu/kaguya/internal/secret"
	sqlite3 "github.com/mattn/go-sqlite3"
)

var errInjectedFailure = errors.New("injected database failure")

// faultPattern 描述一次按出现次数触发的语句故障。
type faultPattern struct {
	substr string
	nth    int
	seen   int
}

// faultDriver 包装真实 SQLite 驱动，按语句内容注入一次失败，用于验证转换事务回滚。
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

var faultDriverSeq atomic.Int64

// newTestDB 创建带固定表结构的临时数据库；测试不访问真实数据库或密钥文件。
func newTestDB(t *testing.T, driverName string) (context.Context, *sql.DB, string) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "provider-secrets-*.db")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	path := file.Name()
	conn, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Exec(`
		CREATE TABLE kaguya_provider_info (id TEXT PRIMARY KEY, api_key TEXT);
		CREATE TABLE kaguya_system_info (id TEXT PRIMARY KEY, tls_certificate_pem TEXT, tls_private_key_pem TEXT);
	`); err != nil {
		t.Fatal(err)
	}
	return context.Background(), conn, path
}

func legacyKey(t *testing.T) ([]byte, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	derived, err := hkdf.Key(sha256.New, der, []byte(legacySaltLabel), legacyInfoLabel, legacyKeyLength)
	if err != nil {
		t.Fatal(err)
	}
	return derived, pemText
}

// legacyEncrypt 复现旧实现的密文格式，用于构造迁移输入。
func legacyEncrypt(t *testing.T, key []byte, plain string) string {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	return legacyV1Prefix + base64.RawStdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(plain), nil))
}

func insertProvider(t *testing.T, ctx context.Context, conn *sql.DB, id, apiKey string) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, "INSERT INTO kaguya_provider_info (id, api_key) VALUES (?, ?)", id, apiKey); err != nil {
		t.Fatal(err)
	}
}

func setLegacyTLS(t *testing.T, ctx context.Context, conn *sql.DB, pemText string) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, "INSERT INTO kaguya_system_info (id, tls_certificate_pem, tls_private_key_pem) VALUES ('global', 'cert', ?)", pemText); err != nil {
		t.Fatal(err)
	}
}

func storedKey(t *testing.T, ctx context.Context, conn *sql.DB, id string) string {
	t.Helper()
	var value string
	if err := conn.QueryRowContext(ctx, "SELECT api_key FROM kaguya_provider_info WHERE id = ?", id).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func targetCipher(t *testing.T) *secret.Cipher {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.NewKeyCipher(raw)
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func TestMigrateProviderSecretsConvertsLegacyFormats(t *testing.T) {
	ctx, conn, _ := newTestDB(t, "sqlite3")
	legacyKeyBytes, legacyPEM := legacyKey(t)
	target := targetCipher(t)

	legacyValue := "sk-legacy-certificate-value"
	v2Value := "sk-already-v2-value"
	insertProvider(t, ctx, conn, "legacy", legacyEncrypt(t, legacyKeyBytes, legacyValue))
	insertProvider(t, ctx, conn, "plain", "sk-historical-plaintext")
	insertProvider(t, ctx, conn, "empty", "")
	v2, err := target.Encrypt(v2Value)
	if err != nil {
		t.Fatal(err)
	}
	insertProvider(t, ctx, conn, "current", v2)
	setLegacyTLS(t, ctx, conn, legacyPEM)

	result, err := migrate(ctx, conn, "", target)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if result.reencrypted != 1 || result.encrypted != 1 || result.verified != 1 || result.empty != 1 {
		t.Fatalf("unexpected stats: %+v", result)
	}
	for id, want := range map[string]string{"legacy": legacyValue, "plain": "sk-historical-plaintext", "current": v2Value} {
		value := storedKey(t, ctx, conn, id)
		if !secret.IsEncrypted(value) {
			t.Fatalf("%s must be stored as enc:v2:, got %q", id, value)
		}
		plain, err := target.Decrypt(value)
		if err != nil || plain != want {
			t.Fatalf("%s plaintext = %q err=%v, want %q", id, plain, err, want)
		}
	}
	if shape := storedKey(t, ctx, conn, "empty"); shape != "" {
		t.Fatalf("empty value must stay empty, got %q", shape)
	}
	var cert, key string
	if err := conn.QueryRowContext(ctx, "SELECT tls_certificate_pem, tls_private_key_pem FROM kaguya_system_info WHERE id = 'global'").Scan(&cert, &key); err != nil {
		t.Fatal(err)
	}
	if cert != "" || key != "" {
		t.Fatal("legacy TLS key material must be cleared in the same transaction")
	}

	// 在已迁移副本上重复执行必须不改变结果。
	first := map[string]string{}
	for _, id := range []string{"legacy", "plain", "current"} {
		first[id] = storedKey(t, ctx, conn, id)
	}
	result, err = migrate(ctx, conn, "", target)
	if err != nil {
		t.Fatalf("re-run migrate: %v", err)
	}
	if result.verified != 3 || result.reencrypted != 0 || result.encrypted != 0 {
		t.Fatalf("unexpected second-run stats: %+v", result)
	}
	for id, want := range first {
		if got := storedKey(t, ctx, conn, id); got != want {
			t.Fatalf("%s changed on re-run", id)
		}
	}
}

func TestMigrateProviderSecretsUsesExplicitLegacyKey(t *testing.T) {
	ctx, conn, _ := newTestDB(t, "sqlite3")
	explicit := make([]byte, 32)
	if _, err := rand.Read(explicit); err != nil {
		t.Fatal(err)
	}
	insertProvider(t, ctx, conn, "external", legacyEncrypt(t, explicit, "sk-from-external-key"))
	target := targetCipher(t)

	if _, err := migrate(ctx, conn, base64.StdEncoding.EncodeToString(explicit), target); err != nil {
		t.Fatalf("migrate with explicit legacy key: %v", err)
	}
	plain, err := target.Decrypt(storedKey(t, ctx, conn, "external"))
	if err != nil || plain != "sk-from-external-key" {
		t.Fatalf("plaintext = %q err=%v", plain, err)
	}
}

func TestMigrateProviderSecretsRollsBackOnWriteFailure(t *testing.T) {
	ctx, conn, path := newTestDB(t, "sqlite3")
	_, legacyPEM := legacyKey(t)
	insertProvider(t, ctx, conn, "first", "sk-first-plaintext")
	insertProvider(t, ctx, conn, "second", "sk-second-plaintext")
	insertProvider(t, ctx, conn, "third", "sk-third-plaintext")
	setLegacyTLS(t, ctx, conn, legacyPEM)
	target := targetCipher(t)

	fault := &faultDriver{base: &sqlite3.SQLiteDriver{}}
	name := fmt.Sprintf("provider-secrets-fault-%d", faultDriverSeq.Add(1))
	sql.Register(name, fault)
	faultConn, err := sql.Open(name, path)
	if err != nil {
		t.Fatal(err)
	}
	defer faultConn.Close()

	// 第一条更新成功、第二条失败：整个事务必须回滚。
	fault.failOnNth("UPDATE kaguya_provider_info", 2)
	if _, err := migrate(ctx, faultConn, "", target); err == nil {
		t.Fatal("migration must fail when a provider write fails")
	}
	for _, id := range []string{"first", "second", "third"} {
		if value := storedKey(t, ctx, conn, id); value != "sk-"+id+"-plaintext" {
			t.Fatalf("failed migration left a rewritten value for %s: %q", id, value)
		}
	}
	var pemText string
	if err := conn.QueryRowContext(ctx, "SELECT tls_private_key_pem FROM kaguya_system_info WHERE id = 'global'").Scan(&pemText); err != nil {
		t.Fatal(err)
	}
	if pemText == "" {
		t.Fatal("failed migration cleared the legacy TLS private key")
	}
}

func TestMigrateProviderSecretsRejectsUnknownCiphertext(t *testing.T) {
	ctx, conn, _ := newTestDB(t, "sqlite3")
	insertProvider(t, ctx, conn, "future", "enc:v9:AAAA")
	if _, err := migrate(ctx, conn, "", targetCipher(t)); err == nil {
		t.Fatal("unknown ciphertext version must fail the migration")
	}
	if value := storedKey(t, ctx, conn, "future"); value != "enc:v9:AAAA" {
		t.Fatalf("failed migration changed the stored value: %q", value)
	}
}
