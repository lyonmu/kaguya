//go:build windows

package workspace

import "os"

// OpenNonblock 在 Windows 上退化为普通只读打开；FIFO 语义由平台文件模型决定。
func OpenNonblock(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY, 0)
}

// ClearNonblock 在 Windows 上无需恢复任何标志。
func ClearNonblock(*os.File) {}
