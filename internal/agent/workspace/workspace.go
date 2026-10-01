// Package workspace 提供 agent 文件工具与项目浏览共享的安全文件原语：
// 路径规范化、非阻塞 regular-file 打开、有界文本窗口读取与 ignore 匹配。
//
// 它只依赖标准库，不 import service/Ent/dto/memory 包；调用方保留各自的
// policy（工具分页、预览截断、文档拒绝、bash 临时目录例外）。
package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// CleanRelative 把调用方输入规范化为工作区内相对路径：拒绝空值、NUL、
// 超长、绝对路径与清理后仍越级的路径。合法的 a/../b 允许规范化为 b。
func CleanRelative(path string, maxLen int) (string, error) {
	if path == "" || len(path) > maxLen || strings.ContainsRune(path, 0) {
		return "", errors.New("path is invalid")
	}
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes workspace")
	}
	return clean, nil
}

// Open 打开一个 pinned 工作区根目录；base 先解析为绝对路径与实际符号链接目标。
func Open(base string) (*os.Root, error) {
	abs, err := filepath.Abs(base)
	if err != nil {
		return nil, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	return os.OpenRoot(abs)
}
