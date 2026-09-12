package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// 目录、符号链接逃逸与普通文本文件的行为保持既有边界。
func TestContentTypeBoundaries(t *testing.T) {
	svc, home, _ := setupGitTest(t)
	dir := filepath.Join(home, "content-project")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	symlinkSupported := true
	if err := os.Symlink(outside, filepath.Join(dir, "escape.txt")); err != nil {
		symlinkSupported = false
	}
	id := createProject(t, svc, dir)

	resp, err := svc.Content(context.Background(), id, "ok.txt")
	if err != nil || resp.Content != "hello\n" || resp.Binary || resp.Truncated {
		t.Fatalf("regular file: %+v err=%v", resp, err)
	}
	if _, err := svc.Content(context.Background(), id, "sub"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("directory must be rejected: %v", err)
	}
	if symlinkSupported {
		if _, err := svc.Content(context.Background(), id, "escape.txt"); err == nil {
			t.Fatal("symlink escaping the project root must fail")
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.Content(canceled, id, "ok.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context must be reported: %v", err)
	}
}
