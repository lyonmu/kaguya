package config

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/lyonmu/kaguya/internal/consts"
)

func TestSQLiteDefaults(t *testing.T) {
	for _, key := range []string{"DB_KIND", "DB_PATH", "DB_KEY_FILE"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	var cli Cli
	parser, err := kong.New(&cli)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if cli.DB.Kind != consts.SQLite || cli.DB.Path != "~/.kaguya/kaguya.db" || cli.DB.KeyFile != "" {
		t.Fatalf("unexpected defaults: kind=%q path=%q", cli.DB.Kind, cli.DB.Path)
	}
}

func TestSQLitePathAndCreation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg := DatabaseConfig{Path: "~/.kaguya/kaguya.db", KeyFile: testSQLiteKeyFile(t)}
	if err := cfg.EnsureSQLiteDatabase(); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".kaguya", "kaguya.db")
	if cfg.Path != want {
		t.Fatalf("path = %q, want %q", cfg.Path, want)
	}
	if runtime.GOOS != "windows" {
		for path, mode := range map[string]os.FileMode{filepath.Dir(want): 0o700, want: 0o600} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != mode {
				t.Fatalf("%s permissions = %o, want %o", path, info.Mode().Perm(), mode)
			}
		}
	}
	if err := os.WriteFile(want, []byte("existing data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureSQLiteDatabase(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(want)
	if err != nil || string(data) != "existing data" {
		t.Fatalf("existing data changed: %q, %v", data, err)
	}
}

func TestSQLiteDSN(t *testing.T) {
	for _, path := range []string{"relative dir/a?#%.db", filepath.Join(t.TempDir(), "a?#%.db")} {
		cfg := DatabaseConfig{Path: path, KeyFile: testSQLiteKeyFile(t)}
		dsn, err := cfg.SQLiteDSN()
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		want, err := filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
		if u.Scheme != "file" || u.Path != filepath.ToSlash(want) || u.Fragment != "" {
			t.Fatalf("unexpected DSN: %s", dsn)
		}
		query := u.Query()
		if query.Get("_busy_timeout") != "5000" || query.Get("_journal_mode") != "WAL" || query.Get("_foreign_keys") != "on" || query.Get("_synchronous") != "FULL" {
			t.Fatal("unexpected connection settings")
		}
		if query.Get("key") != "x'"+strings.Repeat("ab", 32)+"'" {
			t.Fatal("missing or invalid raw SQLCipher key")
		}
	}
}

func TestSQLiteInvalidPath(t *testing.T) {
	for _, path := range []string{"", "~other/kaguya.db"} {
		cfg := DatabaseConfig{Path: path, KeyFile: testSQLiteKeyFile(t)}
		if _, err := cfg.SQLiteDSN(); err == nil {
			t.Fatalf("expected DSN error for %q", path)
		}
		if err := cfg.EnsureSQLiteDatabase(); err == nil {
			t.Fatalf("expected creation error for %q", path)
		}
	}
	cfg := DatabaseConfig{Path: t.TempDir(), KeyFile: testSQLiteKeyFile(t)}
	if err := cfg.EnsureSQLiteDatabase(); err == nil {
		t.Fatal("expected error for a directory")
	}
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	cfg.Path = "~/.kaguya/kaguya.db"
	if _, err := cfg.SQLiteDSN(); err == nil {
		t.Fatal("expected missing home error")
	}
}

func TestPostgreSQLDSN(t *testing.T) {
	cfg := DatabaseConfig{
		Host:     "2001:db8::1",
		Port:     5432,
		User:     "user@example.com",
		Password: "p@ss/word",
		DBName:   "kaguya db",
		SSLMode:  "require",
	}

	got := cfg.PostgreSQLDSN()
	want := "postgres://user%40example.com:p%40ss%2Fword@[2001:db8::1]:5432/kaguya%20db?sslmode=require"
	if got != want {
		t.Fatalf("PostgreSQLDSN() = %q, want %q", got, want)
	}
}

func TestPostgreSQLDSNDefaults(t *testing.T) {
	cfg := DatabaseConfig{
		Host:   "localhost",
		User:   "postgres",
		DBName: "kaguya",
	}

	got := cfg.PostgreSQLDSN()
	want := "postgres://postgres:@localhost:5432/kaguya?sslmode=disable"
	if got != want {
		t.Fatalf("PostgreSQLDSN() = %q, want %q", got, want)
	}
}

func TestMySQLDSNDefaultsPort(t *testing.T) {
	cfg := DatabaseConfig{
		Host:     "localhost",
		User:     "root",
		Password: "secret",
		DBName:   "kaguya",
	}

	got := cfg.MySQLDSN()
	want := "root:secret@tcp(localhost:3306)/kaguya?charset=utf8mb4&parseTime=true&loc=Local"
	if got != want {
		t.Fatalf("MySQLDSN() = %q, want %q", got, want)
	}
}
