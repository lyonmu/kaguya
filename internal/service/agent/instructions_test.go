package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobalInstructions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "AGENTS.md")
	if err := os.WriteFile(path, []byte("use project tests"), 0600); err != nil {
		t.Fatal(err)
	}
	text, err := globalInstructions([]string{"~/missing.md", "~/AGENTS.md", path})
	if err != nil || strings.Count(text, "use project tests") != 1 {
		t.Fatalf("instructions=%q err=%v", text, err)
	}
	if text, err := globalInstructions([]string{}); err != nil || text != "" {
		t.Fatalf("empty=%q %v", text, err)
	}
	if _, err := globalInstructions([]string{home}); err == nil {
		t.Fatal("directory accepted")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 256*1024+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := globalInstructions([]string{path}); err == nil {
		t.Fatal("oversized instructions accepted")
	}
}
