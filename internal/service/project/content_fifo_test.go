//go:build !windows

package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// 项目内的 FIFO 无写端时，普通 Open 会永久阻塞且不受 context 取消影响。
// 预览必须非阻塞打开、随后 fstat 拒绝非普通文件。
func TestContentRejectsFIFOWithoutBlocking(t *testing.T) {
	svc, home, _ := setupGitTest(t)
	dir := filepath.Join(home, "fifo-project")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	id := createProject(t, svc, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, err := svc.Content(ctx, id, "pipe")
	if err == nil {
		t.Fatal("fifo must not be treated as a readable text file")
	}
	if !errors.Is(err, ErrInvalid) && !errors.Is(err, syscall.ENXIO) && !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("fifo preview blocked for %s", elapsed)
	}
}
