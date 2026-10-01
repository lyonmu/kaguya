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
	// 全局指令不再有内容长度上限：超过旧 256KiB 也必须成功。
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 256*1024+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if text, err := globalInstructions([]string{path}); err != nil || len(text) <= 256*1024 {
		t.Fatalf("large global instructions rejected: %d %v", len(text), err)
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

func TestSplitInstructionsFreezeProjectAndKeepGlobalDynamic(t *testing.T) {
	ctx, client := setupChatTest(t)
	global, project := t.TempDir(), t.TempDir()
	globalPath := filepath.Join(global, "AGENTS.md")
	if err := os.WriteFile(globalPath, []byte("global-v1"), 0600); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(project, "agents.md")
	if err := os.WriteFile(projectPath, []byte("project-v1"), 0600); err != nil {
		t.Fatal(err)
	}
	paths := []string{globalPath}

	// 新会话：动态全局 + 待冻结的当前项目快照。
	bundle, err := conversationInstructions(ctx, "fresh", paths, project)
	if err != nil || bundle.format != "split" || bundle.projectSnapshot == nil || !strings.Contains(*bundle.projectSnapshot, "project-v1") {
		t.Fatalf("bundle=%+v err=%v", bundle, err)
	}
	if !strings.Contains(bundle.text, "global-v1") || !strings.Contains(bundle.text, "project-v1") {
		t.Fatalf("text=%q", bundle.text)
	}
	// 首个成功轮次冻结项目快照（含空字符串也必须冻结）。
	turn := testCompletedTurn("fresh", 0)
	turn.ProjectInstructions = bundle.projectSnapshot
	turn.InstructionsFormat = bundle.format
	turn.GlobalInstructionHash = bundle.globalHash
	if err := saveCompletedTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	row, err := client.KaguyaConversation.Get(ctx, "fresh")
	if err != nil || row.InstructionsFormat != "split" || row.ProjectInstructions == nil || !strings.Contains(*row.ProjectInstructions, "project-v1") {
		t.Fatalf("row=%+v err=%v", row, err)
	}
	// 全局文件变化从下一轮生效；项目快照不重读。
	if err := os.WriteFile(globalPath, []byte("global-v2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath, []byte("project-v2"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := conversationInstructions(ctx, "fresh", paths, project)
	if err != nil || !strings.Contains(next.text, "global-v2") || strings.Contains(next.text, "project-v2") {
		t.Fatalf("dynamic global/frozen project failed: %q %v", next.text, err)
	}
	if next.projectSnapshot != nil {
		t.Fatal("frozen project was captured again")
	}
}

func TestLegacyInstructionsAreNotSilentlySplit(t *testing.T) {
	ctx, client := setupChatTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := t.TempDir()
	project := filepath.Join(home, "legacy-workspace")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := client.KaguyaProject.Create().SetID("project-legacy").SetName("legacy").SetPath(project).Save(ctx); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(global, "AGENTS.md")
	projectPath := filepath.Join(project, "agents.md")
	if err := os.WriteFile(globalPath, []byte("global-new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath, []byte("project-new"), 0600); err != nil {
		t.Fatal(err)
	}
	legacy := "Global instructions from /old/AGENTS.md:\n\nlegacy-merged"
	projectID := "project-legacy"
	if _, err := client.KaguyaConversation.Create().SetID("legacy-project").SetTitle(defaultConversationTitle).
		SetModelID("m").SetModelName("M").SetLastMessageAt(time.Now()).
		SetProjectID(projectID).SetAgentInstructions(legacy).SetInstructionsFormat("legacy").Save(ctx); err != nil {
		t.Fatal(err)
	}
	// 旧项目非空快照保持 opaque，不拆解也不重读项目文件。
	bundle, err := conversationInstructions(ctx, "legacy-project", []string{globalPath}, project)
	if err != nil || !bundle.legacy || bundle.text != legacy {
		t.Fatalf("legacy bundle=%+v err=%v", bundle, err)
	}
	// 旧普通会话忽略合并文本，直接使用动态全局并空项目快照。
	if _, err := client.KaguyaConversation.Create().SetID("legacy-plain").SetTitle(defaultConversationTitle).
		SetModelID("m").SetModelName("M").SetLastMessageAt(time.Now()).
		SetAgentInstructions(legacy).SetInstructionsFormat("legacy").Save(ctx); err != nil {
		t.Fatal(err)
	}
	plain, err := conversationInstructions(ctx, "legacy-plain", []string{globalPath}, "")
	if err != nil || plain.legacy || !strings.Contains(plain.text, "global-new") {
		t.Fatalf("plain legacy bundle=%+v err=%v", plain, err)
	}

	// 显式升级：归档旧文本、冻结当前项目指令、切换 split。
	svc := &AgentSvc{}
	resp, err := svc.ConversationInstructionsUpgrade(ctx, "legacy-project")
	if err != nil || resp.InstructionsFormat != "split" || resp.InstructionsLegacy {
		t.Fatalf("upgrade resp=%+v err=%v", resp, err)
	}
	row, err := client.KaguyaConversation.Get(ctx, "legacy-project")
	if err != nil || row.AgentInstructions == nil || *row.AgentInstructions != legacy ||
		row.ProjectInstructions == nil || !strings.Contains(*row.ProjectInstructions, "project-new") {
		t.Fatalf("upgraded row=%+v err=%v", row, err)
	}
	upgraded, err := conversationInstructions(ctx, "legacy-project", []string{globalPath}, project)
	if err != nil || upgraded.legacy || !strings.Contains(upgraded.text, "global-new") || !strings.Contains(upgraded.text, "project-new") {
		t.Fatalf("upgraded bundle=%+v err=%v", upgraded, err)
	}
}
