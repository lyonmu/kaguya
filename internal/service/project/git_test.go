package project

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"entgo.io/ent/dialect"
	"github.com/lyonmu/kaguya/internal/db"
	dto "github.com/lyonmu/kaguya/internal/dto/project"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
)

type gitTestID struct{ n atomic.Int64 }

func (g *gitTestID) GenID() (int64, error) { return g.n.Add(1), nil }

// setupGitTest 初始化临时 HOME、内存数据库，并返回已初始化 git 的项目目录。
func setupGitTest(t *testing.T) (*ProjectSvc, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	client, err := ent.Open(dialect.SQLite, "file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	if err := client.Schema.Create(context.Background(), migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldID, oldLogger := db.EntClient, global.Id, global.Logger
	db.EntClient, global.Id, global.Logger = client, &gitTestID{}, zap.NewNop()
	t.Cleanup(func() { db.EntClient, global.Id, global.Logger = oldClient, oldID, oldLogger })
	dir := filepath.Join(home, "repo")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.name", "tester")
	gitRun(t, dir, "config", "user.email", "tester@example.com")
	return &ProjectSvc{}, home, dir
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func createProject(t *testing.T, svc *ProjectSvc, path string) string {
	t.Helper()
	resp, err := svc.Save(context.Background(), "", &dto.SaveReq{Name: "repo", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	return resp.ID
}

func TestGitStatusAndDiff(t *testing.T) {
	svc, _, dir := setupGitTest(t)
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "a\n")
	write("c.go", "c\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "init")
	write("a.go", "a\nb\n")
	write("new.txt", "x\n")
	if err := os.Remove(filepath.Join(dir, "c.go")); err != nil {
		t.Fatal(err)
	}
	id := createProject(t, svc, dir)

	status, err := svc.GitStatus(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !status.IsGit || status.Branch == "" || len(status.Head) != 7 || status.Truncated {
		t.Fatalf("status=%+v", status)
	}
	byPath := map[string]dto.GitFile{}
	for _, file := range status.Files {
		byPath[file.Path] = file
	}
	if file := byPath["a.go"]; file.Status != "modified" || file.Staged || file.Additions != 1 || file.Deletions != 0 {
		t.Fatalf("a.go=%+v", file)
	}
	if file := byPath["new.txt"]; file.Status != "untracked" {
		t.Fatalf("new.txt=%+v", file)
	}
	if file := byPath["c.go"]; file.Status != "deleted" {
		t.Fatalf("c.go=%+v", file)
	}

	diff, err := svc.GitDiff(context.Background(), id, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if diff.Path != "a.go" || !strings.Contains(diff.Diff, "+b") || diff.Binary || diff.Truncated {
		t.Fatalf("diff=%+v", diff)
	}
	untracked, err := svc.GitDiff(context.Background(), id, "new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(untracked.Diff, "+x") || !strings.Contains(untracked.Diff, "new file mode") {
		t.Fatalf("untracked=%+v", untracked)
	}
	deleted, err := svc.GitDiff(context.Background(), id, "c.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(deleted.Diff, "-c") {
		t.Fatalf("deleted=%+v", deleted)
	}
	for _, path := range []string{"../outside.txt", "/etc/passwd", ""} {
		if _, err := svc.GitDiff(context.Background(), id, path); !errors.Is(err, ErrInvalid) {
			t.Fatalf("diff accepted %q: %v", path, err)
		}
	}
}

func TestGitStatusNonRepository(t *testing.T) {
	svc, home, _ := setupGitTest(t)
	plain := filepath.Join(home, "plain")
	if err := os.Mkdir(plain, 0700); err != nil {
		t.Fatal(err)
	}
	id := createProject(t, svc, plain)
	status, err := svc.GitStatus(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if status.IsGit || status.Message == "" || len(status.Files) != 0 {
		t.Fatalf("status=%+v", status)
	}
	if _, err := svc.GitDiff(context.Background(), id, "a.txt"); !errors.Is(err, ErrNotGit) {
		t.Fatalf("diff=%v", err)
	}
}

func TestTreeAndContent(t *testing.T) {
	svc, home, _ := setupGitTest(t)
	dir := filepath.Join(home, "files")
	for _, item := range []string{"src", "src/deep", ".git", "node_modules"} {
		if err := os.MkdirAll(filepath.Join(dir, item), 0700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"main.go":             "package main\n",
		".hidden":             "hidden\n",
		"src/app.ts":          "export {}\n",
		"src/deep/x.py":       "x = 1\n",
		"src/.cache":          "cache\n",
		"node_modules/lib.js": "module\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0, 1, 2}, 0600); err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("a", maxContentBytes-1) + "中尾"
	if err := os.WriteFile(filepath.Join(dir, "large.txt"), []byte(large), 0600); err != nil {
		t.Fatal(err)
	}
	id := createProject(t, svc, dir)

	tree, err := svc.Tree(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Name != "files" || tree.Truncated || len(tree.Items) != 4 {
		t.Fatalf("tree=%+v", tree)
	}
	for _, item := range tree.Items {
		if item.Name == "node_modules" || strings.HasPrefix(item.Name, ".") {
			t.Fatalf("excluded item=%+v", item)
		}
	}
	// 目录排在文件之前，隐藏项被跳过。
	if !tree.Items[0].IsDir || tree.Items[0].Name != "src" || tree.Items[0].Path != "src" {
		t.Fatalf("first=%+v", tree.Items[0])
	}
	var src *dto.TreeItem
	for _, item := range tree.Items {
		if item.Name == "src" {
			src = item
		}
	}
	if src == nil || len(src.Children) != 2 || src.Children[0].Name != "deep" || src.Children[1].Name != "app.ts" {
		t.Fatalf("src=%+v", src)
	}

	content, err := svc.Content(context.Background(), id, "src/app.ts")
	if err != nil {
		t.Fatal(err)
	}
	if content.Content != "export {}\n" || content.Binary || content.Truncated {
		t.Fatalf("content=%+v", content)
	}
	binary, err := svc.Content(context.Background(), id, "blob.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !binary.Binary || binary.Content != "" {
		t.Fatalf("binary=%+v", binary)
	}
	truncated, err := svc.Content(context.Background(), id, "large.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !truncated.Truncated || !utf8.ValidString(truncated.Content) || len(truncated.Content) != maxContentBytes-1 {
		t.Fatalf("truncated len=%d valid=%t", len(truncated.Content), utf8.ValidString(truncated.Content))
	}
	for _, path := range []string{"../secret", "/etc/hostname", "", "src/../.."} {
		if _, err := svc.Content(context.Background(), id, path); !errors.Is(err, ErrInvalid) {
			t.Fatalf("content accepted %q: %v", path, err)
		}
	}
}

func TestParsePorcelainV2AndNumstat(t *testing.T) {
	status := []byte("# branch.oid 9e1be7defe7e82247e770cb091e5d090fa53757a\x00" +
		"# branch.head main\x00" +
		"1 .M N... 100644 100644 100644 a a a.go\x00" +
		"2 R. N... 100644 100644 100644 a a R100 renamed.go\x00main.go\x00" +
		"? sub/new.txt\x00" +
		"u UU N... 100644 100644 100644 100644 a a a conflict.go\x00")
	branch, head, entries := parsePorcelainV2(status)
	if branch != "main" || head != "9e1be7d" || len(entries) != 4 {
		t.Fatalf("branch=%s head=%s entries=%+v", branch, head, entries)
	}
	if entries[0].status() != "modified" || entries[0].staged() {
		t.Fatalf("modified=%+v", entries[0])
	}
	if entries[1].status() != "renamed" || !entries[1].staged() || entries[1].path != "renamed.go" {
		t.Fatalf("renamed=%+v", entries[1])
	}
	if entries[2].status() != "untracked" || entries[3].status() != "conflicted" {
		t.Fatalf("untracked=%+v conflicted=%+v", entries[2], entries[3])
	}
	stats := parseNumstat([]byte("1\t2\ta.go\x000\t0\t\x00main.go\x00renamed.go\x00-\t-\timg.png\x00"))
	if stats["a.go"] != [2]int{1, 2} || stats["renamed.go"] != [2]int{0, 0} {
		t.Fatalf("stats=%+v", stats)
	}
	if _, exists := stats["img.png"]; exists {
		t.Fatalf("binary included: %+v", stats)
	}
}
