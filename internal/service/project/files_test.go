package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyonmu/kaguya/internal/agent/workspace"
	dto "github.com/lyonmu/kaguya/internal/dto/project"
)

func TestFilesPageUsesStrictSharedReferenceFilter(t *testing.T) {
	svc, _, dir := setupGitTest(t)
	for name, body := range map[string]string{
		".gitignore": "private.txt\nignored/\n", "private.txt": "private", "safe.txt": "safe",
		"ignored/child.txt": "private", "sub/.gitignore": "nested.txt\n", "sub/nested.txt": "private", "sub/safe.txt": "safe",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	id := createProject(t, svc, dir)
	page, err := svc.FilesPage(context.Background(), &dto.FilesReq{ID: id, Query: ".txt"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range page.Files {
		if name == "private.txt" || name == "ignored/child.txt" || name == "sub/nested.txt" {
			t.Fatalf("filtered candidate %q", name)
		}
	}
	if len(page.Files) != 2 {
		t.Fatalf("files=%v", page.Files)
	}
	if err := os.Remove(filepath.Join(dir, "sub/.gitignore")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub/.gitignore"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FilesPage(context.Background(), &dto.FilesReq{ID: id}); !errors.Is(err, workspace.ErrReferenceIgnore) {
		t.Fatal(err)
	}
}

func writeSearchFiles(t *testing.T, dir string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("item-%03d.txt", i)), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFilesPageExactPaginationAndRoot(t *testing.T) {
	for _, count := range []int{50, 51, 100} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			svc, _, dir := setupGitTest(t)
			writeSearchFiles(t, dir, count)
			id := createProject(t, svc, dir)
			cursor := ""
			seen := map[string]bool{}
			for {
				page, err := svc.FilesPage(context.Background(), &dto.FilesReq{ID: id, Query: "item-", Cursor: cursor})
				if err != nil || page.CursorReset || page.ScanIncomplete {
					t.Fatalf("unexpected page state: page=%+v err=%v", page, err)
				}
				if len(page.Files) > 50 || len(page.Files) != len(page.Items) {
					t.Fatal("display contract/legacy fields changed")
				}
				for _, item := range page.Files {
					if seen[item] {
						t.Fatal("cursor repeated a candidate")
					}
					seen[item] = true
				}
				if page.NextCursor == "" {
					if page.Truncated {
						t.Fatal("complete final page falsely truncated")
					}
					break
				}
				if page.NextCursor == cursor || !page.Truncated {
					t.Fatal("cursor did not advance")
				}
				cursor = page.NextCursor
			}
			if len(seen) != count {
				t.Fatalf("enumerated=%d want=%d", len(seen), count)
			}
			for _, query := range []string{"", "."} {
				page, err := svc.FilesPage(context.Background(), &dto.FilesReq{ID: id, Query: query, IncludeDirectories: true})
				if err != nil || len(page.Items) == 0 || page.Items[0].Type != "directory" || page.Items[0].Path != "." || page.Items[0].Name != "." {
					t.Fatalf("root candidate missing: page=%+v err=%v", page, err)
				}
			}
		})
	}
}

func TestFilesPageCursorIdentityAndInvalidReset(t *testing.T) {
	svc, home, dir := setupGitTest(t)
	writeSearchFiles(t, dir, 60)
	id := createProject(t, svc, dir)
	first, err := svc.FilesPage(context.Background(), &dto.FilesReq{ID: id, Query: "item-"})
	if err != nil || first.NextCursor == "" {
		t.Fatal("fixture did not produce a cursor")
	}
	cursor, ok := decodeFileSearchCursor(first.NextCursor)
	if !ok || strings.Contains(first.NextCursor, dir) {
		t.Fatal("invalid/host-path exposing cursor")
	}
	for name, alter := range map[string]func(*fileSearchCursor){
		"project":     func(c *fileSearchCursor) { c.ProjectID = "other" },
		"root":        func(c *fileSearchCursor) { c.Root = "other" },
		"digest":      func(c *fileSearchCursor) { c.Digest = "other" },
		"query":       func(c *fileSearchCursor) { c.Query = "other" },
		"directories": func(c *fileSearchCursor) { c.Directories = true },
		"version":     func(c *fileSearchCursor) { c.Version++ },
		"order":       func(c *fileSearchCursor) { c.Order++ },
		"negative":    func(c *fileSearchCursor) { c.Offset = -1 },
		"overflow":    func(c *fileSearchCursor) { c.Offset = int(^uint(0) >> 1) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cursor
			alter(&changed)
			page, err := svc.FilesPage(context.Background(), &dto.FilesReq{ID: id, Query: "item-", Cursor: encodeFileSearchCursor(changed)})
			if err != nil || !page.CursorReset || page.Files[0] != first.Files[0] {
				t.Fatalf("identity mismatch not reset: page=%+v err=%v", page, err)
			}
		})
	}
	page, err := svc.FilesPage(context.Background(), &dto.FilesReq{ID: id, Query: "item-", Cursor: "broken"})
	if err != nil || !page.CursorReset || page.Files[0] != first.Files[0] {
		t.Fatal("malformed cursor not reset")
	}
	other := filepath.Join(home, "other")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	writeSearchFiles(t, other, 60)
	otherID := createProject(t, svc, other)
	page, err = svc.FilesPage(context.Background(), &dto.FilesReq{ID: otherID, Query: "item-", Cursor: first.NextCursor})
	if err != nil || !page.CursorReset {
		t.Fatal("different project accepted another project's cursor")
	}
	// 修改候选内容但不修改根目录：集合元信息摘要必须使旧页失效。
	if err := os.WriteFile(filepath.Join(dir, "item-000.txt"), []byte("changed-size"), 0600); err != nil {
		t.Fatal(err)
	}
	page, err = svc.FilesPage(context.Background(), &dto.FilesReq{ID: id, Query: "item-", Cursor: first.NextCursor})
	if err != nil || !page.CursorReset || page.Files[0] != first.Files[0] {
		t.Fatal("candidate metadata change did not reset")
	}
	// 目录替换后即使路径和mtime相同，真实Dev/Ino身份也必须使旧页失效。
	rootInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+".previous"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeSearchFiles(t, dir, 60)
	if err := os.Chtimes(dir, rootInfo.ModTime(), rootInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	page, err = svc.FilesPage(context.Background(), &dto.FilesReq{ID: id, Query: "item-", Cursor: first.NextCursor})
	if err != nil || !page.CursorReset {
		t.Fatal("replaced root accepted stale identity")
	}
}

func TestFilesPagePartialTerminatesAndCancellation(t *testing.T) {
	svc, _, dir := setupGitTest(t)
	writeSearchFiles(t, dir, 1000)
	id := createProject(t, svc, dir)
	cursor := ""
	seen := map[string]bool{}
	for pageIndex := 0; ; pageIndex++ {
		if pageIndex > 20 {
			t.Fatal("partial terminal page looped")
		}
		page, err := svc.FilesPage(context.Background(), &dto.FilesReq{ID: id, Query: "item-", Cursor: cursor})
		if err != nil || !page.ScanIncomplete || !page.Truncated || page.CursorReset {
			t.Fatalf("not an honest partial page: page=%+v err=%v", page, err)
		}
		for _, name := range page.Files {
			if seen[name] {
				t.Fatal("partial page repeated")
			}
			seen[name] = true
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			t.Fatal("partial cursor failed to advance")
		}
		cursor = page.NextCursor
	}
	if len(seen) == 0 || len(seen) >= 1000 {
		t.Fatal("fixture did not exercise bounded candidate retention")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.FilesPage(ctx, &dto.FilesReq{ID: id}); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation became success/partial: %v", err)
	}
}

func TestFilesPageEmptyPartial(t *testing.T) {
	svc, _, dir := setupGitTest(t)
	for i := 0; i < 1000; i++ {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("directory-%04d-", i)+strings.Repeat("x", 100)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	id := createProject(t, svc, dir)
	page, err := svc.FilesPage(context.Background(), &dto.FilesReq{ID: id, Query: "no-match"})
	if err != nil || !page.ScanIncomplete || !page.Truncated || page.NextCursor != "" || len(page.Items) != 0 {
		t.Fatalf("empty partial manufactured cursor: page=%+v err=%v", page, err)
	}
}

func TestFilesPageObservedDirectoryChangeResetsCursor(t *testing.T) {
	svc, _, dir := setupGitTest(t)
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	writeSearchFiles(t, nested, 60)
	id := createProject(t, svc, dir)
	first, err := svc.FilesPage(context.Background(), &dto.FilesReq{ID: id, Query: "item-"})
	if err != nil || first.NextCursor == "" {
		t.Fatal("fixture did not produce a cursor")
	}
	// 不匹配的子项使已观察目录发生变化，根目录及候选文件内容均不变。
	if err := os.WriteFile(filepath.Join(nested, "unrelated.bin"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	page, err := svc.FilesPage(context.Background(), &dto.FilesReq{ID: id, Query: "item-", Cursor: first.NextCursor})
	if err != nil || !page.CursorReset || page.Files[0] != first.Files[0] {
		t.Fatalf("observed directory mutation not reset: page=%+v err=%v", page, err)
	}
}

// 候选搜索：旧 files 字段保持文件字符串；include_directories 追加目录；
// cursor 与 query/include_directories 绑定，不匹配时从第一页重新开始。
func TestFilesPageCandidatesAndCursor(t *testing.T) {
	svc, _, dir := setupGitTest(t)
	for _, name := range []string{"alpha.go", "beta.go", "gamma.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package x\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "alphabet"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0700); err != nil {
		t.Fatal(err)
	}
	id := createProject(t, svc, dir)

	page, err := svc.FilesPage(context.Background(), &dto.FilesReq{ID: id, Query: "alpha", IncludeDirectories: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Files) != 1 || page.Files[0] != "alpha.go" {
		t.Fatalf("files=%v", page.Files)
	}
	var sawDirectory bool
	for _, item := range page.Items {
		if item.Type == "directory" && item.Path == "alphabet" {
			sawDirectory = true
		}
	}
	if !sawDirectory {
		t.Fatalf("items=%+v", page.Items)
	}

	// 游标与查询绑定：不同 query 携带旧 cursor 必须从第一页开始。
	all, err := svc.FilesPage(context.Background(), &dto.FilesReq{ID: id, Query: ".go"})
	if err != nil {
		t.Fatal(err)
	}
	if all.Truncated {
		t.Fatal("small result set must not be truncated")
	}
	if all.NextCursor != "" {
		t.Fatalf("unexpected cursor: %q", all.NextCursor)
	}
}

func TestFilesPageCursorPagination(t *testing.T) {
	svc, _, dir := setupGitTest(t)
	for index := 0; index < 60; index++ {
		name := filepath.Join(dir, "file-"+string(rune('a'+index%26))+string(rune('a'+index/26))+".txt")
		if err := os.WriteFile(name, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	id := createProject(t, svc, dir)
	first, err := svc.FilesPage(context.Background(), &dto.FilesReq{ID: id, Query: "file-"})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Truncated || first.NextCursor == "" || len(first.Files) != fileSearchPageSize {
		t.Fatalf("first page: files=%d truncated=%v cursor=%q", len(first.Files), first.Truncated, first.NextCursor)
	}
	second, err := svc.FilesPage(context.Background(), &dto.FilesReq{ID: id, Query: "file-", Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Files) != 10 || second.Truncated {
		t.Fatalf("second page: files=%d truncated=%v", len(second.Files), second.Truncated)
	}
	if second.Files[0] == first.Files[0] {
		t.Fatal("second page must not repeat the first page")
	}
}
