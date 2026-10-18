//go:build !windows

package instructions

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// FIFO 无写端时普通 open 会永久阻塞；非阻塞打开必须立即失败于 regular 校验。
func TestLoadRejectsFIFO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(context.Background(), []string{path}); err == nil {
		t.Fatal("FIFO accepted as global instructions")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
