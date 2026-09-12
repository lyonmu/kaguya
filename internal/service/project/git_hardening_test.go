package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 配置了 textconv 的仓库在“只看 diff”时也不能执行外部程序；#O03 隔离实验确认
// 缺少 --no-textconv 时转换程序会被调用。
func TestGitDiffDoesNotRunTextconv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("textconv helper script requires a POSIX shell")
	}
	svc, _, dir := setupGitTest(t)
	target := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(target, []byte("raw-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("data.txt diff=probe\n"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "marker.txt")
	helper := filepath.Join(dir, "helper.sh")
	script := "#!/bin/sh\nprintf 'executed\\n' >> \"" + marker + "\"\ncat \"$1\"\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "config", "diff.probe.textconv", helper)
	gitRun(t, dir, "config", "diff.probe.binary", "false")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "init")
	if err := os.WriteFile(target, []byte("raw-v2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	id := createProject(t, svc, dir)

	start := time.Now()
	diff, err := svc.GitDiff(context.Background(), id, "data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("textconv helper executed by read-only diff, marker=%s", marker)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	// 查看类命令同时禁用外部 diff 与 fsmonitor，不应调用任何仓库配置的程序。
	status, err := svc.GitStatus(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("textconv helper executed by status, marker=%s", marker)
	}
	if !strings.Contains(diff.Diff, "+raw-v2") || !strings.Contains(diff.Diff, "-raw-v1") {
		t.Fatalf("raw diff missing: %+v", diff)
	}
	if len(status.Files) == 0 {
		t.Fatalf("status lost files: %+v", status)
	}
	if elapsed := time.Since(start); elapsed > gitTimeout {
		t.Fatalf("read-only git commands took %s", elapsed)
	}
}

// 配置的 fsmonitor 钩子同样不能在只读查看时执行。
func TestGitStatusDoesNotRunFsmonitorHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fsmonitor helper script requires a POSIX shell")
	}
	svc, _, dir := setupGitTest(t)
	marker := filepath.Join(dir, "fsmonitor-ran.txt")
	helper := filepath.Join(dir, "fsmonitor.sh")
	script := "#!/bin/sh\nprintf 'ran\\n' >> \"" + marker + "\"\nprintf '/\\0'\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "config", "core.fsmonitor", helper)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	id := createProject(t, svc, dir)

	if _, err := svc.GitStatus(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("fsmonitor hook executed during read-only status, marker=%s", marker)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

// 进程环境里的 GIT_DIR/GIT_WORK_TREE 不能改写只读查看的目标仓库。
func TestGitIgnoresRedirectingEnvironment(t *testing.T) {
	svc, home, dir := setupGitTest(t)
	if err := os.WriteFile(filepath.Join(dir, "project.txt"), []byte("project\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// 诱饵仓库里有同名文件但内容不同，误用环境变量时会读到它。
	decoy := filepath.Join(home, "decoy")
	if err := os.Mkdir(decoy, 0700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, decoy, "init", "-q")
	gitRun(t, decoy, "config", "user.name", "decoy")
	gitRun(t, decoy, "config", "user.email", "decoy@example.com")
	if err := os.WriteFile(filepath.Join(decoy, "project.txt"), []byte("decoy\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, decoy, "add", ".")
	gitRun(t, decoy, "commit", "-qm", "decoy")
	gitRun(t, decoy, "config", "core.worktree", decoy)

	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)
	id := createProject(t, svc, dir)

	status, err := svc.GitStatus(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !status.IsGit {
		t.Fatalf("status=%+v", status)
	}
	found := false
	for _, file := range status.Files {
		if file.Path == "project.txt" {
			found = true
		}
		if strings.Contains(file.Path, "decoy") {
			t.Fatalf("status leaked decoy repository entry: %+v", status.Files)
		}
	}
	if !found {
		t.Fatalf("status missing project file: %+v", status.Files)
	}
	if status.Head != "" {
		t.Fatalf("status used the decoy HEAD: %q", status.Head)
	}
}

// 含通配符等特殊字符的文件名必须按字面路径匹配，不能解释成 pathspec 查询范围。
func TestGitDiffTreatsPathsLiterally(t *testing.T) {
	svc, _, dir := setupGitTest(t)
	names := []string{"a*b.txt", "c[d].txt", "e:f.txt", "space name.txt", "中文 名.txt", "u\\v.txt"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "init")
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"-changed\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	id := createProject(t, svc, dir)

	for _, name := range names {
		diff, err := svc.GitDiff(context.Background(), id, name)
		if err != nil {
			t.Fatalf("diff %q: %v", name, err)
		}
		if diff.Path != name || !strings.Contains(diff.Diff, "diff --git") {
			t.Fatalf("diff %q=%+v", name, diff)
		}
		if strings.Count(diff.Diff, "diff --git") != 1 {
			t.Fatalf("path %q matched more than its own file: %+v", name, diff)
		}
	}
	status, err := svc.GitStatus(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, file := range status.Files {
		seen[file.Path] = true
	}
	for _, name := range names {
		if !seen[name] {
			t.Fatalf("status missing %q: %+v", name, status.Files)
		}
	}
}

// 项目根位于仓库子目录时，diff 使用相对仓库根的路径，响应仍返回项目相对路径。
func TestGitDiffInRepositorySubdirectory(t *testing.T) {
	svc, _, repo := setupGitTest(t)
	sub := filepath.Join(repo, "nested", "project")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "inner.txt"), []byte("v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-qm", "init")
	if err := os.WriteFile(filepath.Join(sub, "inner.txt"), []byte("v2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	id := createProject(t, svc, sub)

	status, err := svc.GitStatus(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Files) != 1 || status.Files[0].Path != "inner.txt" || status.Files[0].Status != "modified" {
		t.Fatalf("subdirectory status: %+v", status.Files)
	}
	diff, err := svc.GitDiff(context.Background(), id, "inner.txt")
	if err != nil {
		t.Fatal(err)
	}
	if diff.Path != "inner.txt" || !strings.Contains(diff.Diff, "+v2") {
		t.Fatalf("subdirectory diff: %+v", diff)
	}
}
