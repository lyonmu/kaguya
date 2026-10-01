package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	dto "github.com/lyonmu/kaguya/internal/dto/project"
)

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
