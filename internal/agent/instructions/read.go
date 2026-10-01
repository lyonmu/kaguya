package instructions

import (
	"fmt"
	"io"
	"os"
)

// openRegular 非阻塞打开全局指令文件并确认是普通文件，避免 FIFO 永久阻塞 open。
func openRegular(path string) (*os.File, os.FileInfo, error) {
	file, err := openNonblock(path)
	if err != nil {
		return nil, nil, err
	}
	clearNonblock(file)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, fmt.Errorf("stat global instructions %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, fmt.Errorf("instructions %q must be a regular file", path)
	}
	return file, info, nil
}

func readAll(file *os.File) ([]byte, error) {
	return io.ReadAll(file)
}
