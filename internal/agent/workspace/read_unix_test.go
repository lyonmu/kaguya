//go:build !windows

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// FIFO 无写端时普通 open 会阻塞且不受 context 取消影响；非阻塞打开必须立即
// 返回，随后由 regular-file 检查拒绝。
func TestReadRejectsFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	root := newRoot(t, dir)

	file, err := OpenNonblock(root, "pipe")
	if err != nil {
		t.Fatalf("nonblocking open of FIFO failed: %v", err)
	}
	if _, statErr := file.Stat(); statErr != nil {
		t.Fatal(statErr)
	}
	file.Close()

	if _, _, err := OpenRegular(root, "pipe"); err == nil {
		t.Fatal("FIFO accepted as regular file")
	}
	if _, err := ReadTextWindow(context.Background(), root, "pipe", 0, 10, 1<<20, 1024); err == nil {
		t.Fatal("FIFO accepted by ReadTextWindow")
	}
	if _, err := ReadAll(context.Background(), root, "pipe", 1<<20); err == nil {
		t.Fatal("FIFO accepted by ReadAll")
	}
}

func TestOpenNonblockMissingFile(t *testing.T) {
	root := newRoot(t, t.TempDir())
	if _, err := OpenNonblock(root, "missing.txt"); !os.IsNotExist(err) {
		t.Fatalf("expected not-exist, got %v", err)
	}
}
