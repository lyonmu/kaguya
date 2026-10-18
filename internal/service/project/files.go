package project

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lyonmu/kaguya/internal/agent/workspace"
	dtoproject "github.com/lyonmu/kaguya/internal/dto/project"
)

// 候选分页不是可解析文件数量上限；不建立跨请求索引。
const fileSearchPageSize = 50
const fileSearchMetadataBytes = 64 << 10
const fileSearchCursorVersion = 1
const fileSearchOrderVersion = 1

type fileSearchCursor struct {
	Version     int    `json:"v"`
	ProjectID   string `json:"p"`
	Query       string `json:"q"`
	Directories bool   `json:"d"`
	Root        string `json:"r"`
	Order       int    `json:"s"`
	Digest      string `json:"h"`
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
	if raw == "" || len(raw) > 4096 {
		return fileSearchCursor{}, false
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return fileSearchCursor{}, false
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var cursor fileSearchCursor
	if err := decoder.Decode(&cursor); err != nil || cursor.Offset < 0 || cursor.Version != fileSearchCursorVersion || cursor.Order != fileSearchOrderVersion {
		return fileSearchCursor{}, false
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fileSearchCursor{}, false
	}
	return cursor, true
}

// Files保持旧files字符串协议，内部仍返回第一展示页。
func (s *ProjectSvc) Files(ctx context.Context, id, query string) (*FileSearchResp, error) {
	return s.FilesPage(ctx, &dtoproject.FilesReq{ID: id, Query: query})
}

type fileCandidate struct {
	dtoproject.ReferenceCandidate
	Size     int64  `json:"size"`
	Mode     uint32 `json:"mode"`
	Modified int64  `json:"modified"`
	Pivot    bool   `json:"pivot"`
}

// root指纹只向客户端返回SHA-256；atime不参与，避免读取本身让游标失效。
// 从已验证的真实根目录句柄取得平台身份，不用路径或mtime冒充file ID。
func fileSearchRootDigest(workspacePath string, file *os.File, info os.FileInfo) (string, error) {
	identity, err := fileSearchIdentity(file, info)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(struct {
		Path     string           `json:"path"`
		Identity fileSearchRootID `json:"identity"`
		Mode     uint32           `json:"mode"`
		Size     int64            `json:"size"`
		Modified int64            `json:"modified"`
	}{workspacePath, identity, uint32(info.Mode()), info.Size(), info.ModTime().UnixNano()})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// FilesPage每次确认DB归属并在os.Root中重新扫描；cursor不授予访问权限。
func (s *ProjectSvc) FilesPage(ctx context.Context, req *dtoproject.FilesReq) (*FileSearchResp, error) {
	if req == nil || len(req.Query) > 512 || strings.ContainsRune(req.Query, 0) || len(req.Cursor) > 4096 {
		return nil, ErrInvalid
	}
	workspacePath, err := s.Workspace(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(workspacePath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	scanCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	matcher, err := workspace.NewIgnoreMatcher(scanCtx, root)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, workspace.ErrReferenceIgnore
	}
	filter := workspace.NewReferenceFilter(scanCtx, root, matcher)
	rootFile, proof, err := filter.OpenDirectory(".", true)
	if err != nil {
		return nil, err
	}
	rootDigest, err := fileSearchRootDigest(workspacePath, rootFile, proof.Info)
	rootFile.Close()
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(filepath.ToSlash(req.Query))
	budget := &workspace.ReferenceMetadataBudget{Limit: fileSearchMetadataBytes}
	// 固定扫描状态、digest/cursor元信息也占用共享预算。
	// LastPath允许4096字节并可能需要JSON转义；cursor/扫描状态也计费。
	if !budget.Take(512 + 6*4096 + int64(len(strconv.Quote(req.Query)))) {
		return nil, workspace.ErrReferenceMetadataBudget
	}
	candidates := make([]fileCandidate, 0)
	directoryDigest := sha256.New()
	directoryEncoder := json.NewEncoder(directoryDigest)
	add := func(kind, name string, info os.FileInfo) error {
		// 观察到的目录变更元信息流式进入digest，不保存全部目录集合。
		if kind == "directory" {
			if err := directoryEncoder.Encode(struct {
				Path     string
				Size     int64
				Mode     uint32
				Modified int64
			}{name, info.Size(), uint32(info.Mode()), info.ModTime().UnixNano()}); err != nil {
				return err
			}
		}
		if kind == "directory" && !req.IncludeDirectories || !strings.Contains(strings.ToLower(filepath.ToSlash(name)), query) {
			return nil
		}
		base := filepath.Base(name)
		item := fileCandidate{ReferenceCandidate: dtoproject.ReferenceCandidate{Type: kind, Path: filepath.ToSlash(name), Name: base}, Size: info.Size(), Mode: uint32(info.Mode()), Modified: info.ModTime().UnixNano(), Pivot: strings.HasPrefix(strings.ToLower(base), query)}
		data, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if !budget.Take(int64(len(data) + 32)) {
			return workspace.ErrReferenceMetadataBudget
		}
		candidates = append(candidates, item)
		return nil
	}
	if err := add("directory", ".", proof.Info); err != nil {
		return nil, err
	}
	scan, err := filter.WalkReferences(".", budget, add)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	incomplete := scan.Incomplete || len(scan.Warnings) > 0 || err != nil
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Pivot != candidates[j].Pivot {
			return candidates[i].Pivot
		}
		return candidates[i].Path < candidates[j].Path
	})
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	// 每个bounded候选编码到hash，不再分配整个集合的JSON副本。
	for _, item := range candidates {
		if err := encoder.Encode(item); err != nil {
			return nil, err
		}
	}
	if err := encoder.Encode(struct {
		Directories string
		Incomplete  bool
		Reason      string
		LastPath    string
		Warnings    []string
	}{hex.EncodeToString(directoryDigest.Sum(nil)), incomplete, scan.Reason, scan.LastPath, scan.Warnings}); err != nil {
		return nil, err
	}
	expected := fileSearchCursor{Version: fileSearchCursorVersion, ProjectID: req.ID, Query: req.Query, Directories: req.IncludeDirectories, Root: rootDigest, Order: fileSearchOrderVersion, Digest: hex.EncodeToString(digest.Sum(nil))}
	// 极端转义query也必须遵守HTTP cursor长度合同，不能生成不可回传的游标。
	cursorForSize := expected
	cursorForSize.Offset = int(^uint(0) >> 1)
	if len(encodeFileSearchCursor(cursorForSize)) > 4096 {
		return nil, ErrInvalid
	}
	offset := 0
	reset := false
	if req.Cursor != "" {
		cursor, ok := decodeFileSearchCursor(req.Cursor)
		if ok && cursor.ProjectID == expected.ProjectID && cursor.Query == expected.Query && cursor.Directories == expected.Directories && cursor.Root == expected.Root && cursor.Digest == expected.Digest && cursor.Offset <= len(candidates) {
			offset = cursor.Offset
		} else {
			reset = true
		}
	}
	end := min(offset+fileSearchPageSize, len(candidates))
	result := &FileSearchResp{Files: []string{}, Items: []dtoproject.ReferenceCandidate{}, ScanIncomplete: incomplete, CursorReset: reset}
	for _, item := range candidates[offset:end] {
		result.Items = append(result.Items, item.ReferenceCandidate)
		if item.Type == "file" {
			result.Files = append(result.Files, item.Path)
		}
	}
	result.Truncated = incomplete || end < len(candidates)
	// partial终页必须终止，不能以truncated生成不前进的cursor。
	if end < len(candidates) {
		expected.Offset = end
		result.NextCursor = encodeFileSearchCursor(expected)
	}
	return result, nil
}

type FileSearchResp struct {
	Files          []string                        `json:"files"`
	Items          []dtoproject.ReferenceCandidate `json:"items"`
	Truncated      bool                            `json:"truncated"`
	NextCursor     string                          `json:"next_cursor,omitempty"`
	ScanIncomplete bool                            `json:"scan_incomplete"`
	CursorReset    bool                            `json:"cursor_reset,omitempty"`
}
