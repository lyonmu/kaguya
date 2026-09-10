package initialize

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/consts"
)

func keyInitializationConfig(t *testing.T) (config.DatabaseConfig, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return config.DatabaseConfig{Kind: consts.SQLite, Path: "~/.kaguya/kaguya.db"}, filepath.Join(home, ".kaguya", "kaguya.key")
}

func TestSQLCipherKeyFirstStartAndReuse(t *testing.T) {
	cfg, path := keyInitializationConfig(t)
	if err := SQLCipherKey(&cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString(strings.TrimSuffix(string(data), "\n"))
	if err != nil || len(decoded) != 32 || len(data) != 65 {
		t.Fatal("generated key has invalid encoding or size")
	}
	if err := cfg.ValidateSQLiteKey(); err != nil {
		t.Fatal(err)
	}
	if cfg.KeyFile != config.DefaultSQLiteKeyFile {
		t.Fatalf("unexpected key path: %q", cfg.KeyFile)
	}
	if runtime.GOOS != "windows" {
		for name, mode := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
			info, err := os.Stat(name)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("unexpected permissions for %s", name)
			}
		}
	}
	dbPath, err := cfg.SQLitePath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatal("key initialization should not create the database")
	}
	if err := os.WriteFile(dbPath, []byte("existing database"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		// Fresh config simulates parsing CLI defaults on the next startup.
		cfg.KeyFile = ""
		if err := SQLCipherKey(&cfg); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, after) {
			t.Fatal("existing key was replaced")
		}
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".kaguya-key-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatal("temporary key files were left behind")
	}
}

func TestSQLCipherKeyMissingWithDatabase(t *testing.T) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		t.Run("database"+suffix, func(t *testing.T) {
			cfg, keyPath := keyInitializationConfig(t)
			dbPath := filepath.Join(t.TempDir(), "custom.db")
			cfg.Path = dbPath
			// Even an empty database or an orphan sidecar must prevent key replacement.
			if err := os.WriteFile(dbPath+suffix, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := SQLCipherKey(&cfg); err == nil || !strings.Contains(err.Error(), "restore the original key") {
				t.Fatalf("expected lost-key error, got %v", err)
			}
			if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
				t.Fatal("generated a replacement key")
			}
			if cfg.KeyFile != "" {
				t.Fatal("failed initialization changed the configuration")
			}
		})
	}
}

func TestSQLCipherKeyExplicitPath(t *testing.T) {
	cfg, defaultPath := keyInitializationConfig(t)
	for _, path := range []string{config.DefaultSQLiteKeyFile, filepath.Join(t.TempDir(), "custom.key")} {
		cfg.KeyFile = path
		if err := SQLCipherKey(&cfg); err == nil {
			t.Fatal("missing explicit key should not be generated")
		}
	}
	if _, err := os.Stat(defaultPath); !os.IsNotExist(err) {
		t.Fatal("generated default key for explicit configuration")
	}
	data := []byte(strings.Repeat("ab", 32))
	if err := os.WriteFile(cfg.KeyFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SQLCipherKey(&cfg); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(cfg.KeyFile)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("custom key was replaced")
	}
}

func TestSQLCipherKeyInvalidExistingFile(t *testing.T) {
	cfg, path := keyInitializationConfig(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte("invalid existing key")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SQLCipherKey(&cfg); err == nil {
		t.Fatal("invalid key accepted")
	} else if strings.Contains(err.Error(), string(data)) {
		t.Fatal("error leaked key contents")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("invalid existing key was overwritten")
	}
}

func TestSQLCipherKeyConcurrentInitialization(t *testing.T) {
	base, path := keyInitializationConfig(t)
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for range cap(errors) {
		wg.Go(func() {
			cfg := base
			errors <- SQLCipherKey(&cfg)
		})
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	base.KeyFile = path
	if err := base.ValidateSQLiteKey(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLCipherKeySkipsOtherBackends(t *testing.T) {
	cfg, path := keyInitializationConfig(t)
	for _, kind := range []consts.DBKind{consts.MySQL, consts.PostgreSQL, consts.Postgres} {
		cfg.Kind = kind
		if err := SQLCipherKey(&cfg); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created SQLite key for another backend")
	}
}
