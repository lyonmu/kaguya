package instructions

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// 每次底层读取至多 64KiB；取消可在相邻读取间及时生效，不限制全局正文大小。
func readAll(ctx context.Context, file *os.File) ([]byte, error) {
	return io.ReadAll(instructionReader{ctx: ctx, file: file})
}

type instructionReader struct {
	ctx  context.Context
	file *os.File
}

func (r instructionReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > 64*1024 {
		p = p[:64*1024]
	}
	return r.file.Read(p)
}

// 仅保留匹配的文件名，目录枚举分批检查取消，不积累无关目录项。
func instructionCandidates(ctx context.Context, path string) ([]string, error) {
	dir, err := os.Open(filepath.Dir(path))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list global instructions %q: %w", path, err)
	}
	defer dir.Close()
	var candidates []string
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := dir.ReadDir(256)
		for _, entry := range entries {
			if strings.EqualFold(entry.Name(), "AGENTS.md") {
				candidates = append(candidates, filepath.Join(filepath.Dir(path), entry.Name()))
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(candidates)
	return candidates, nil
}
