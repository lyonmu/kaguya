package db_test

import (
	"bytes"
	"context"
	"database/sql"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/db"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	// Independent plaintext engine, used only to prove encrypted files cannot be read.
	_ "modernc.org/sqlite"
)

func encryptedConfig(t *testing.T) config.DatabaseConfig {
	t.Helper()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	if err := os.WriteFile(keyFile, []byte(strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return config.DatabaseConfig{Path: filepath.Join(dir, "nested", "data ?#%.db"), KeyFile: keyFile}
}

func openCipher(t *testing.T, cfg *config.DatabaseConfig) *sql.DB {
	t.Helper()
	dsn, err := cfg.SQLiteDSN()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal("open cipher connection failed")
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestInitSQLiteWALAndPersistence(t *testing.T) {
	cfg := encryptedConfig(t)
	if err := cfg.EnsureSQLiteDatabase(); err != nil {
		t.Fatal(err)
	}
	client, err := db.InitSQLite(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Exercise application data through Ent, including a transaction.
	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const marker = "kaguya-encryption-test-secret-987654321"
	if err := tx.KaguyaSystemInfo.Create().SetID("global").SetSystemPrompt(marker).Exec(ctx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Keep the writer open: committed data may be in WAL instead of the main file.
	for _, path := range []string{cfg.Path, cfg.Path + "-wal"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(marker)) || bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
			t.Fatalf("plaintext found in %s", filepath.Base(path))
		}
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	client, err = db.InitSQLite(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	row, err := client.KaguyaSystemInfo.Get(ctx, "global")
	if err != nil || row.SystemPrompt != marker {
		t.Fatal("encrypted Ent record did not survive reopening")
	}

	conn := openCipher(t, &cfg)
	// Distinct physical connections must each receive the key and connection settings.
	for range 2 {
		c, err := conn.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		var mode, version string
		if err := c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("journal_mode = %q, err = %v", mode, err)
		}
		if err := c.QueryRowContext(ctx, "PRAGMA cipher_version").Scan(&version); err != nil || !strings.HasPrefix(version, "4.") {
			t.Fatalf("SQLCipher 4 is required, got %q, err = %v", version, err)
		}
		for pragma, want := range map[string]int{"busy_timeout": 5000, "foreign_keys": 1, "synchronous": 2} {
			var got int
			if err := c.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
				t.Fatalf("%s = %d, want %d, err = %v", pragma, got, want, err)
			}
		}
		var count int
		if err := c.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&count); err != nil || count == 0 {
			t.Fatal("connection could not decrypt schema")
		}
	}
	// The independent modernc SQLite engine has no cipher codec.
	plainURI := url.URL{Scheme: "file", Path: filepath.ToSlash(cfg.Path)}
	plain, err := sql.Open("sqlite", plainURI.String())
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	var count int
	if err := plain.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&count); err == nil {
		t.Fatal("ordinary SQLite could read the encrypted schema")
	}
}

func TestDebugDoesNotLogDatabaseSecrets(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	cfg := encryptedConfig(t)
	if err := cfg.EnsureSQLiteDatabase(); err != nil {
		t.Fatal(err)
	}
	client, err := db.InitSQLite(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	const secret = "private-system-prompt-should-not-be-logged"
	if err := client.KaguyaSystemInfo.Create().SetID("global").SetSystemPrompt(secret).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), secret) {
		t.Fatal("debug log contains database secret")
	}
}

func TestInitSQLiteWrongOrMissingKey(t *testing.T) {
	cfg := encryptedConfig(t)
	if err := cfg.EnsureSQLiteDatabase(); err != nil {
		t.Fatal(err)
	}
	client, err := db.InitSQLite(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	originalKey := cfg.KeyFile
	wrongKey := strings.Repeat("cd", 32)
	wrongFile := filepath.Join(t.TempDir(), "wrong-key")
	if err := os.WriteFile(wrongFile, []byte(wrongKey), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{wrongFile, "", filepath.Join(t.TempDir(), "missing")} {
		cfg.KeyFile = path
		client, err := db.InitSQLite(&cfg)
		if err == nil {
			_ = client.Close()
			t.Fatal("invalid key accepted")
		}
		if strings.Contains(err.Error(), wrongKey) || strings.Contains(err.Error(), strings.Repeat("ab", 32)) {
			t.Fatal("error leaked a key")
		}
		after, err := os.ReadFile(cfg.Path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("failed open modified the encrypted database")
		}
	}
	cfg.KeyFile = originalKey
	client, err = db.InitSQLite(&cfg)
	if err != nil {
		t.Fatal("original key no longer opens database")
	}
	_ = client.Close()
}

func TestInitSQLiteRejectsPlaintext(t *testing.T) {
	cfg := encryptedConfig(t)
	if err := cfg.EnsureSQLiteDatabase(); err != nil {
		t.Fatal(err)
	}
	plainURI := url.URL{Scheme: "file", Path: filepath.ToSlash(cfg.Path)}
	plain, err := sql.Open("sqlite", plainURI.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Exec("CREATE TABLE existing_data (value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := plain.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	client, err := db.InitSQLite(&cfg)
	if err == nil {
		_ = client.Close()
		t.Fatal("plaintext database accepted")
	}
	after, err := os.ReadFile(cfg.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("plaintext database was modified")
	}
}

func TestInitSQLiteInvalidPath(t *testing.T) {
	cfg := encryptedConfig(t)
	cfg.Path = t.TempDir()
	if client, err := db.InitSQLite(&cfg); err == nil {
		_ = client.Close()
		t.Fatal("expected error for a directory")
	}
}
