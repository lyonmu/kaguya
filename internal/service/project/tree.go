package project

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	dto "github.com/lyonmu/kaguya/internal/dto/project"
)

const (
	treeMaxItems     = 5000
	treeMaxDepth     = 16
	maxContentBytes  = 512 << 10
	binarySniffBytes = 8 << 10
)

// cleanRelative 将请求中的路径规范化为项目根内的相对路径，拒绝绝对路径和越级。
func cleanRelative(path string) (string, error) {
	if path == "" || len(path) > 4096 || strings.ContainsRune(path, 0) {
		return "", ErrInvalid
	}
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrInvalid
	}
	return clean, nil
}

// Tree 遍历项目根并返回嵌套文件树，包含隐藏项，按 .gitignore / .dockerignore 过滤，
// 并跳过 .git 与常见依赖/产物目录。
func (s *ProjectSvc) Tree(ctx context.Context, id string) (*dto.TreeResp, error) {
	dir, err := s.Workspace(ctx, id)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	ignore := newIgnoreMatcher(root)
	children := map[string][]*dto.TreeItem{}
	visited := 0
	resp := &dto.TreeResp{Name: filepath.Base(dir), Items: []*dto.TreeItem{}}
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		name := entry.Name()
		if name == ".git" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() && excludedTreeDir(name) {
			return fs.SkipDir
		}
		if ignore.ignored(path, entry.IsDir()) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		visited++
		if visited > treeMaxItems {
			resp.Truncated = true
			return fs.SkipAll
		}
		parent := "."
		if index := strings.LastIndex(path, "/"); index >= 0 {
			parent = path[:index]
		}
		item := &dto.TreeItem{Name: name, Path: path}
		if entry.IsDir() {
			if strings.Count(path, "/") >= treeMaxDepth {
				return fs.SkipDir
			}
			item.IsDir = true
		} else {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			item.Size = info.Size()
		}
		children[parent] = append(children[parent], item)
		return nil
	})
	if err != nil {
		return nil, err
	}
	var build func(parent string) []*dto.TreeItem
	build = func(parent string) []*dto.TreeItem {
		items := children[parent]
		// WalkDir 已按名称排序，稳定分组让目录始终排在文件之前。
		sort.SliceStable(items, func(i, j int) bool { return items[i].IsDir && !items[j].IsDir })
		for _, item := range items {
			if item.IsDir {
				item.Children = build(item.Path)
			}
		}
		return items
	}
	resp.Items = build(".")
	return resp, nil
}

func excludedTreeDir(name string) bool {
	switch name {
	case "node_modules", "vendor", "target", "dist", "__pycache__":
		return true
	}
	return false
}

// Content 读取项目内的 UTF-8 文本文件，超出上限时截断，二进制内容只返回元信息。
func (s *ProjectSvc) Content(ctx context.Context, id, path string) (*dto.ContentResp, error) {
	dir, err := s.Workspace(ctx, id)
	if err != nil {
		return nil, err
	}
	rel, err := cleanRelative(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// 先非阻塞打开再看类型：项目内的 FIFO 无写端时，普通 Open 会永久阻塞，
	// 且不受 context 取消影响；lstat 预检查也无法阻止检查后的替换。
	file, err := openReadFile(root, rel)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	clearNonblock(file)
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxContentBytes+1))
	if err != nil {
		return nil, err
	}
	resp := &dto.ContentResp{Path: rel, Size: info.Size()}
	if len(data) > maxContentBytes {
		resp.Truncated = true
		data = data[:maxContentBytes]
	}
	if bytes.IndexByte(data[:min(len(data), binarySniffBytes)], 0) >= 0 {
		resp.Binary = true
		return resp, nil
	}
	if !utf8.Valid(data) {
		if resp.Truncated {
			data = validUTF8Prefix(data)
		}
		if !utf8.Valid(data) {
			resp.Binary = true
			return resp, nil
		}
	}
	resp.Content = string(data)
	return resp, nil
}

// validUTF8Prefix 去掉截断读取在末尾留下的不完整编码；文件本身非法时不改动数据。
func validUTF8Prefix(data []byte) []byte {
	for end := len(data); end > 0 && end > len(data)-utf8.UTFMax; end-- {
		if utf8.Valid(data[:end]) {
			return data[:end]
		}
	}
	return data
}
