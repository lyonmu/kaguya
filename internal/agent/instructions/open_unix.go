//go:build !windows

package instructions

import (
	"os"
	"syscall"
)

func openNonblock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

func clearNonblock(file *os.File) {
	_ = syscall.SetNonblock(int(file.Fd()), false)
}
