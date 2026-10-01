package project

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	dtoproject "github.com/lyonmu/kaguya/internal/dto/project"
)

// fileSearchPageSize 是候选展示分页大小；它不是引用可解析文件数量的上限。
const fileSearchPageSize = 50

// fileSearchCursor 是绑定 query/include_directories 的分页游标；不匹配的旧游标
// 直接忽略并从第一页开始，避免把旧筛选的偏移应用到新查询。
type fileSearchCursor struct {
	Query       string `json:"q"`
	Directories bool   `json:"d"`
	Offset      int    `json:"o"`
}

func encodeFileSearchCursor(cursor fileSearchCursor) string {
	data, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeFileSearchCursor(raw string) (fileSearchCursor, bool) {
	if raw == "" {
		return fileSearchCursor{}, false
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return fileSearchCursor{}, false
	}
	var cursor fileSearchCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.Offset < 0 {
		return fileSearchCursor{}, false
	}
	return cursor, true
}

// Files 保持旧契约：只返回普通文件候选，不分页；内部复用 FilesPage。
func (s *ProjectSvc) Files(ctx context.Context, id, query string) (*FileSearchResp, error) {
	return s.FilesPage(ctx, &dtoproject.FilesReq{ID: id, Query: query})
}

// FilesPage 搜索工作区候选：文件始终返回在 files；include_directories=true 时
// items 追加目录候选。新客户端使用 items/next_cursor，旧客户端行为不变。
func (s *ProjectSvc) FilesPage(ctx context.Context, req *dtoproject.FilesReq) (*FileSearchResp, error) {
	if req == nil || len(req.Query) > 512 || strings.ContainsRune(req.Query, 0) {
		return nil, ErrInvalid
	}
	path, err := s.Workspace(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	offset := 0
	if cursor, ok := decodeFileSearchCursor(req.Cursor); ok && cursor.Query == req.Query && cursor.Directories == req.IncludeDirectories {
		offset = cursor.Offset
	}

	query := strings.ToLower(filepath.ToSlash(req.Query))
	type candidate struct {
		kind  string
		path  string
		base  string
		pivot bool // basename 以 query 开头，排序优先
	}
	var candidates []candidate
	walkTruncated := false
	visited := 0
	stop := errors.New("file search limit")
	err = fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		// Dot-prefixed entries are hidden from the mention picker at every depth.
		if name != "." && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		visited++
		if visited > 20000 {
			walkTruncated = true
			return stop
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "vendor", "target", "dist", "__pycache__":
				return fs.SkipDir
			}
			if req.IncludeDirectories && name != "." && strings.Contains(strings.ToLower(filepath.ToSlash(name)), query) {
				candidates = append(candidates, candidate{kind: "directory", path: filepath.ToSlash(name), base: strings.ToLower(d.Name()), pivot: strings.HasPrefix(strings.ToLower(d.Name()), query)})
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.Contains(strings.ToLower(filepath.ToSlash(name)), query) {
			return nil
		}
		candidates = append(candidates, candidate{kind: "file", path: filepath.ToSlash(name), base: strings.ToLower(d.Name()), pivot: strings.HasPrefix(strings.ToLower(d.Name()), query)})
		return nil
	})
	if err != nil && !errors.Is(err, stop) {
		return nil, err
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].pivot != candidates[j].pivot {
			return candidates[i].pivot
		}
		return candidates[i].path < candidates[j].path
	})
	if offset > len(candidates) {
		offset = len(candidates)
	}
	end := min(offset+fileSearchPageSize, len(candidates))
	result := &FileSearchResp{Files: []string{}, Items: []dtoproject.ReferenceCandidate{}}
	for _, item := range candidates[offset:end] {
		result.Items = append(result.Items, dtoproject.ReferenceCandidate{Type: item.kind, Path: item.path, Name: filepath.Base(item.path)})
		if item.kind == "file" {
			result.Files = append(result.Files, item.path)
		}
	}
	result.Truncated = walkTruncated || end < len(candidates)
	if result.Truncated && end > 0 {
		result.NextCursor = encodeFileSearchCursor(fileSearchCursor{Query: req.Query, Directories: req.IncludeDirectories, Offset: end})
	}
	if result.NextCursor == "" {
		result.NextCursor = ""
	}
	return result, nil
}

// FileSearchResp 是新旧客户端共享的候选响应；files 保持旧字符串协议，
// items 提供类型化候选，next_cursor 绑定 query 与 include_directories。
type FileSearchResp struct {
	Files      []string                        `json:"files"`
	Items      []dtoproject.ReferenceCandidate `json:"items"`
	Truncated  bool                            `json:"truncated"`
	NextCursor string                          `json:"next_cursor,omitempty"`
}
