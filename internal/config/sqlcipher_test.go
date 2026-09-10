package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testSQLiteKeyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSQLiteKeyFile(t *testing.T) {
	for _, suffix := range []string{"", "\n", "\r\n"} {
		path := testSQLiteKeyFile(t)
		if err := os.WriteFile(path, []byte(strings.Repeat("cd", 32)+suffix), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := DatabaseConfig{KeyFile: path}
		key, err := cfg.sqliteKey()
		if err != nil || key != strings.Repeat("cd", 32) {
			t.Fatal("valid key rejected")
		}
	}
}

func TestSQLiteKeyFileInvalid(t *testing.T) {
	for _, value := range []string{"", "password", strings.Repeat("z", 64), strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("a", 64) + "\n\n", strings.Repeat("a", 10000)} {
		path := testSQLiteKeyFile(t)
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := DatabaseConfig{KeyFile: path, Path: filepath.Join(t.TempDir(), "new", "kaguya.db")}
		if _, err := cfg.SQLiteDSN(); err == nil {
			t.Fatal("invalid key accepted")
		} else if len(value) >= 32 && strings.Contains(err.Error(), value) {
			t.Fatal("error leaked the key")
		}
		if err := cfg.EnsureSQLiteDatabase(); err == nil {
			t.Fatal("invalid key accepted during creation")
		}
		if _, err := os.Stat(filepath.Dir(cfg.Path)); !os.IsNotExist(err) {
			t.Fatal("invalid key created a database directory")
		}
	}
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
		cfg := DatabaseConfig{KeyFile: path}
		if _, err := cfg.sqliteKey(); err == nil {
			t.Fatal("missing/non-file key accepted")
		}
	}
	if runtime.GOOS != "windows" {
		path := testSQLiteKeyFile(t)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := DatabaseConfig{KeyFile: path}
		if _, err := cfg.sqliteKey(); err == nil {
			t.Fatal("world-readable key accepted")
		}
	}
}

func TestSQLiteKeyHomeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.WriteFile(filepath.Join(home, "key"), []byte(strings.Repeat("ef", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DatabaseConfig{KeyFile: "~/key"}
	if _, err := cfg.sqliteKey(); err != nil {
		t.Fatal(err)
	}
}
