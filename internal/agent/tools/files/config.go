package files

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"charm.land/fantasy"
)

func jsonResponse(v any) fantasy.ToolResponse {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fantasy.NewTextErrorResponse("failed to encode tool response")
	}
	return fantasy.NewTextResponse(string(b))
}

func isTextFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}

	sample := buf[:n]

	if bytes.Contains(sample, []byte{0}) {
		return false, nil
	}

	return true, nil
}

const (
	DefaultReadLines   = 100
	MaxReadLines       = 100
	DefaultMaxEntries  = 200
	MaxEntries         = 500
	DefaultMaxMatches  = 50
	MaxMatches         = 200
	DefaultMaxFileSize = 10 * 1024 * 1024 // 10 MiB，安全兜底，不作为 Agent 分页单位
)

type Config struct {
	RootDir       string
	AllowHidden   bool
	MaxFileBytes  int64
	DenyFileNames map[string]struct{}
	DenyDirs      map[string]struct{}
}

type SafeFS struct {
	root string
	cfg  Config
}

func NewSafeFS(cfg Config) (*SafeFS, error) {
	if cfg.RootDir == "" {
		return nil, errors.New("root dir is required")
	}

	root, err := filepath.Abs(cfg.RootDir)
	if err != nil {
		return nil, err
	}

	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}

	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = DefaultMaxFileSize
	}

	if cfg.DenyFileNames == nil {
		cfg.DenyFileNames = defaultDenyFileNames()
	}

	if cfg.DenyDirs == nil {
		cfg.DenyDirs = defaultDenyDirs()
	}

	return &SafeFS{
		root: root,
		cfg:  cfg,
	}, nil
}

func (s *SafeFS) Tools() []fantasy.AgentTool {
	return []fantasy.AgentTool{
		fantasy.NewParallelAgentTool(
			"list_files",
			"List files in a directory under the workspace root. Returns relative paths only.",
			s.listFiles,
		),
		fantasy.NewParallelAgentTool(
			"read_file",
			"Read a text file by line range. Reads at most 100 lines per call.",
			s.readFile,
		),
		fantasy.NewParallelAgentTool(
			"grep_files",
			"Search text files under the workspace root. Literal search by default; regex is optional.",
			s.grepFiles,
		),
		fantasy.NewParallelAgentTool(
			"file_metadata",
			"Return safe metadata for a file or directory under the workspace root.",
			s.fileMetadata,
		),
	}
}

type ListFilesInput struct {
	Path          string `json:"path,omitempty" description:"Relative directory path. Default is current workspace root."`
	IncludeHidden bool   `json:"include_hidden,omitempty" description:"Whether to include hidden files. Default false."`
	MaxEntries    int    `json:"max_entries,omitempty" description:"Maximum entries to return. Default 200, maximum 500."`
}

type ReadFileInput struct {
	Path      string `json:"path" description:"Relative file path to read."`
	StartLine int    `json:"start_line,omitempty" description:"1-based start line. Default 1."`
	Limit     int    `json:"limit,omitempty" description:"Maximum lines to read. Default 100, maximum 100."`
}

type GrepFilesInput struct {
	Path          string `json:"path,omitempty" description:"Relative directory or file path. Default workspace root."`
	Query         string `json:"query" description:"Search query."`
	UseRegex      bool   `json:"use_regex,omitempty" description:"Use regular expression. Default false."`
	CaseSensitive bool   `json:"case_sensitive,omitempty" description:"Case sensitive search. Default false."`
	MaxMatches    int    `json:"max_matches,omitempty" description:"Maximum matches. Default 50, maximum 200."`
}

type FileMetadataInput struct {
	Path string `json:"path" description:"Relative file or directory path."`
}

type FileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
}

type ReadFileResult struct {
	Path          string   `json:"path"`
	StartLine     int      `json:"start_line"`
	EndLine       int      `json:"end_line"`
	Limit         int      `json:"limit"`
	HasMore       bool     `json:"has_more"`
	NextStartLine int      `json:"next_start_line,omitempty"`
	Lines         []string `json:"lines"`
}

type GrepMatch struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}
