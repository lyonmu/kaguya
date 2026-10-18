//go:build unix

package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	dto "github.com/lyonmu/kaguya/internal/dto/project"
	"golang.org/x/sys/unix"
)

func TestFilesPageSpecialFilesAndForbiddenAncestors(t *testing.T) {
	svc, home, dir := setupGitTest(t)
	for name, body := range map[string]string{
		"safe.txt": "fixture", "src/safe.txt": "fixture", "src/private.txt": "private",
		"src/.gitignore": "private.txt\n", ".gitignore": "ignored.txt\n", "ignored.txt": "private",
		"vendor/private.txt": "private", "target/private.txt": "private", ".GiT/private.txt": "private",
		".hidden": "private", "temp.tmp": "temporary",
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(home, "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	for alias, target := range map[string]string{"external-link": outside, "file-link": "safe.txt", "directory-link": "src"} {
		if err := os.Symlink(target, filepath.Join(dir, alias)); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mkfifo(filepath.Join(dir, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	id := createProject(t, svc, dir)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	page, err := svc.FilesPage(ctx, &dto.FilesReq{ID: id, IncludeDirectories: true})
	if err != nil || page.ScanIncomplete {
		t.Fatalf("special file scan blocked or failed: page=%+v err=%v", page, err)
	}
	want := map[string]string{".": "directory", "src": "directory", "safe.txt": "file", "src/safe.txt": "file"}
	if len(page.Items) != len(want) {
		t.Fatalf("unsafe candidates=%+v", page.Items)
	}
	for _, item := range page.Items {
		if want[item.Path] != item.Type {
			t.Fatalf("forbidden candidate=%+v", item)
		}
	}
}
