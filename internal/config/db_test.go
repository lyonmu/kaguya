package config

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
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
	if cli.DB.Path != "~/.kaguya/kaguya.db" || cli.DB.KeyFile != "" {
		t.Fatalf("unexpected defaults: path=%q keyfile=%q", cli.DB.Path, cli.DB.KeyFile)
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
