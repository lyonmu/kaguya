//go:build !windows

package workspace

import (
	"os"
	"syscall"
)

// OpenNonblock 以 O_NONBLOCK 打开路径：FIFO 无写端时立即返回错误，
// 而不是让读取方永久阻塞在 open 上（open 不受 context 取消影响）。
func OpenNonblock(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

// ClearNonblock 在确认是普通文件后恢复阻塞模式，后续读取不受影响。
func ClearNonblock(file *os.File) {
	_ = syscall.SetNonblock(int(file.Fd()), false)
}
