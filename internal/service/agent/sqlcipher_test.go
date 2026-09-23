package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/ent"
)

func openDiskChatClient(t testing.TB) *ent.Client {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte(strings.Repeat("ab", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DatabaseConfig{Path: filepath.Join(dir, "data.db"), KeyFile: key}
	writer, err := db.InitSQLite(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	return writer
}
