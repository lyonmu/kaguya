package agent

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestGlobalInstructionsRejectFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := globalInstructions([]string{path}); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		// Release a blocking reader so a regression does not leak a goroutine.
		f, _ := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if f != nil {
			f.Close()
		}
		t.Fatal("instruction loading blocked on FIFO")
	}
}

func TestInstructionCaseMatchingAndProjectBoundary(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	globalPath := filepath.Join(global, "agents.MD")
	projectPath := filepath.Join(project, "aGeNtS.md")
	for path, data := range map[string]string{globalPath: "global-marker", projectPath: "project-marker"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	text, err := loadInstructions([]string{filepath.Join(global, "AGENTS.md"), globalPath}, project)
	if err != nil || strings.Count(text, "global-marker") != 1 || strings.Count(text, "project-marker") != 1 || strings.Index(text, "global-marker") > strings.Index(text, "project-marker") {
		t.Fatalf("instructions=%q err=%v", text, err)
	}
	entries := []os.DirEntry{}
	for _, name := range []string{"AGENTS.md", "agents.md", "aGeNtS.MD", "README.md"} {
		// Separate directories work on case-insensitive filesystems as well.
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
		found, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, found...)
	}
	if names := instructionNames(entries); strings.Join(names, ",") != "AGENTS.md,aGeNtS.MD,agents.md" {
		t.Fatalf("names=%v", names)
	}
	if err := os.Remove(projectPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(globalPath, projectPath); err != nil {
		t.Fatal(err)
	}
	if _, err := loadInstructions(nil, project); err == nil {
		t.Fatal("project instruction symlink escaped workspace")
	}
}

func TestConversationInstructionsPersistEmptyAndLegacySnapshots(t *testing.T) {
	ctx, client := setupChatTest(t)
	global, project := t.TempDir(), t.TempDir()
	path := filepath.Join(global, "AGENTS.md")
	paths := []string{path}
	// Even an empty snapshot is initialized, not a request to load again later.
	empty, err := conversationInstructions(ctx, "empty", paths, project)
	if err != nil || empty != "" {
		t.Fatalf("empty=%q err=%v", empty, err)
	}
	turn := testCompletedTurn("empty", 0)
	turn.AgentInstructions = &empty
	if err := saveCompletedTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("global-snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(project, "agents.md")
	if err := os.WriteFile(projectPath, []byte("project-snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := conversationInstructions(ctx, "empty", paths, project); err != nil || got != "" {
		t.Fatalf("empty reloaded: %q %v", got, err)
	}
	// Old rows have NULL; their first successful continuation adopts one snapshot.
	if err := saveCompletedTurn(ctx, testCompletedTurn("legacy", 0)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := conversationInstructions(ctx, "legacy", paths, project)
	if err != nil || !strings.Contains(snapshot, "global-snapshot") || !strings.Contains(snapshot, "project-snapshot") {
		t.Fatalf("snapshot=%q err=%v", snapshot, err)
	}
	turn = testCompletedTurn("legacy", 1)
	turn.AgentInstructions = &snapshot
	if err := saveCompletedTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	row, err := client.KaguyaConversation.Get(ctx, "legacy")
	if err != nil || row.AgentInstructions == nil || *row.AgentInstructions != snapshot {
		t.Fatalf("snapshot not stored: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(projectPath); err != nil {
		t.Fatal(err)
	}
	// Reads exclusively from the database even with invalid filesystem inputs.
	if got, err := conversationInstructions(ctx, "legacy", []string{global}, "not-a-directory"); err != nil || got != snapshot {
		t.Fatalf("snapshot reread: %q %v", got, err)
	}
	if got, err := conversationInstructions(ctx, "fresh", paths, project); err != nil || got != "" {
		t.Fatalf("new chat reused stale snapshot: %q %v", got, err)
	}
}
