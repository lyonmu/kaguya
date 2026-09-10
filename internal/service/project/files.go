package project

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type FileSearchResp struct {
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated"`
}

// Files lists regular files within a pinned workspace; symlinks are never followed.
func (s *ProjectSvc) Files(ctx context.Context, id, query string) (*FileSearchResp, error) {
	if len(query) > 512 || strings.ContainsRune(query, 0) {
		return nil, ErrInvalid
	}
	path, err := s.Workspace(ctx, id)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	result := &FileSearchResp{Files: []string{}}
	query = strings.ToLower(filepath.ToSlash(query))
	visited := 0
	stop := errors.New("file search limit")
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		visited++
		if visited > 20000 {
			result.Truncated = true
			return stop
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".codegraph", ".kaguya", "node_modules", "vendor", "target", "dist", ".next", ".venv", "__pycache__":
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.Contains(strings.ToLower(path), query) {
			return nil
		}
		result.Files = append(result.Files, path)
		if len(result.Files) >= 50 {
			result.Truncated = true
			return stop
		}
		return nil
	})
	if err != nil && !errors.Is(err, stop) {
		return nil, err
	}
	sort.SliceStable(result.Files, func(i, j int) bool {
		a, b := strings.ToLower(filepath.Base(result.Files[i])), strings.ToLower(filepath.Base(result.Files[j]))
		if strings.HasPrefix(a, query) != strings.HasPrefix(b, query) {
			return strings.HasPrefix(a, query)
		}
		return result.Files[i] < result.Files[j]
	})
	return result, nil
}
