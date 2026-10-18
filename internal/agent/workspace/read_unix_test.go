//go:build !windows

package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
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
	if _, err := ReadTextWindow(context.Background(), root, "pipe", 0, 10, 1<<20, 1024, 1024); err == nil {
		t.Fatal("FIFO accepted by ReadTextWindow")
	}
	if _, err := ReadAll(context.Background(), root, "pipe", 1<<20); err == nil {
		t.Fatal("FIFO accepted by ReadAll")
	}
}

func TestIgnoreFilesRejectFIFOWithoutWaitingForWriter(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(fmt.Sprintf("nested=%t", nested), func(t *testing.T) {
			dir := t.TempDir()
			name := ".gitignore"
			if nested {
				if err := os.Mkdir(filepath.Join(dir, "sub"), 0700); err != nil {
					t.Fatal(err)
				}
				name = filepath.Join("sub", name)
			}
			fifo := filepath.Join(dir, name)
			if err := syscall.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}
			root := newRoot(t, dir)
			done := make(chan error, 1)
			go func() {
				matcher, err := NewIgnoreMatcher(context.Background(), root)
				if err == nil && nested {
					_, err = matcher.Match("sub/file.txt", false)
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("strict ignore accepted FIFO")
				}
			case <-time.After(time.Second):
				// 旧实现的阻塞 open 回归时也释放夹具，不遗留读协程。
				fd, err := syscall.Open(fifo, syscall.O_RDWR|syscall.O_NONBLOCK, 0)
				if err == nil {
					_ = syscall.Close(fd)
				}
				t.Fatal("ignore read blocked waiting for a FIFO writer")
			}
			matcher := NewLenientIgnoreMatcher(context.Background(), root)
			if matcher.Ignored("sub/file.txt", false) {
				t.Fatal("lenient ignore changed empty-rule policy")
			}
		})
	}
}

func TestOpenNonblockMissingFile(t *testing.T) {
	root := newRoot(t, t.TempDir())
	if _, err := OpenNonblock(root, "missing.txt"); !os.IsNotExist(err) {
		t.Fatalf("expected not-exist, got %v", err)
	}
}
