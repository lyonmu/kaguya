package files

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
)

func findToolByName(tools []fantasy.AgentTool, name string) fantasy.AgentTool {
	for _, t := range tools {
		if t.Info().Name == name {
			return t
		}
	}
	return nil
}

func projectRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReadFile(t *testing.T) {
	tests := []struct {
		name       string
		rootDir    string
		input      string
		maxBytes   int64
		setup      func(t *testing.T, dir string)
		ctx        context.Context
		wantErr    bool
		wantInResp []string
	}{
		{
			name:     "relative text file",
			rootDir:  "", // set in setup
			input:    `{"path":"hello.txt"}`,
			maxBytes: 1024,
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			ctx:        context.Background(),
			wantInResp: []string{"hello world"},
		},
		{
			name:    "empty path is directory",
			rootDir: "",
			input:   `{"path":"."}`,
			setup: func(t *testing.T, dir string) {
			},
			ctx:     context.Background(),
			wantErr: true,
		},
		{
			name:    "path escape via ../",
			rootDir: "",
			input:   `{"path":"../escape.txt"}`,
			setup: func(t *testing.T, dir string) {
				parent := filepath.Dir(dir)
				if err := os.WriteFile(filepath.Join(parent, "escape.txt"), []byte("secret\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			ctx:     context.Background(),
			wantErr: true,
		},
		{
			name:    "absolute path rejected",
			rootDir: "",
			input:   `{"path":"/etc/hosts"}`,
			setup:   func(t *testing.T, dir string) {},
			ctx:     context.Background(),
			wantErr: true,
		},
		{
			name:    "symlink escape rejected",
			rootDir: "",
			input:   `{"path":"link.txt"}`,
			setup: func(t *testing.T, dir string) {
				parent := filepath.Dir(dir)
				target := filepath.Join(parent, "secret.txt")
				if err := os.WriteFile(target, []byte("secret\n"), 0644); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(dir, "link.txt")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
			},
			ctx:     context.Background(),
			wantErr: true,
		},
		{
			name:    "binary file rejected",
			rootDir: "",
			input:   `{"path":"bin.dat"}`,
			setup: func(t *testing.T, dir string) {
				bin := make([]byte, 64)
				bin[0] = 0x00
				bin[1] = 0x01
				bin[2] = 0xFF
				if err := os.WriteFile(filepath.Join(dir, "bin.dat"), bin, 0644); err != nil {
					t.Fatal(err)
				}
			},
			ctx:     context.Background(),
			wantErr: true,
		},
		{
			name:     "file exceeds MaxFileBytes",
			rootDir:  "",
			input:    `{"path":"big.txt"}`,
			maxBytes: 4,
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte("this is way too long\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			ctx:     context.Background(),
			wantErr: true,
		},
		{
			name:    "cancelled context",
			rootDir: "",
			input:   `{"path":"hello.txt"}`,
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}(),
			wantErr: true,
		},
		{
			name:    "limit exceeds MaxReadLines is capped",
			rootDir: "",
			input:   `{"path":"lines.txt","limit":500}`,
			setup: func(t *testing.T, dir string) {
				var sb strings.Builder
				for i := 0; i < 150; i++ {
					sb.WriteString("line\n")
				}
				if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte(sb.String()), 0644); err != nil {
					t.Fatal(err)
				}
			},
			ctx:        context.Background(),
			wantInResp: []string{`"limit": 100`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			rootDir := tt.rootDir
			if rootDir == "" {
				rootDir = dir
			}
			if tt.setup != nil {
				tt.setup(t, dir)
			}

			maxBytes := tt.maxBytes
			if maxBytes == 0 {
				maxBytes = DefaultMaxFileSize
			}

			safeFS, err := NewSafeFS(Config{
				RootDir:      rootDir,
				MaxFileBytes: maxBytes,
			})
			if err != nil {
				t.Fatal(err)
			}

			readFileTool := findToolByName(safeFS.Tools(), "read_file")
			if readFileTool == nil {
				t.Fatal("read_file tool not found")
			}

			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}

			resp, err := readFileTool.Run(ctx, fantasy.ToolCall{Input: tt.input})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantErr && !resp.IsError {
				t.Fatalf("expected error response, got: %s", resp.Content)
			}

			for _, want := range tt.wantInResp {
				if !strings.Contains(resp.Content, want) {
					t.Fatalf("expected %q in response, got: %s", want, resp.Content)
				}
			}
		})
	}
}

func TestReadFileREADME(t *testing.T) {
	root := projectRoot(t)

	safeFS, err := NewSafeFS(Config{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}

	readFileTool := findToolByName(safeFS.Tools(), "read_file")
	if readFileTool == nil {
		t.Fatal("read_file tool not found")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	response, err := readFileTool.Run(ctx, fantasy.ToolCall{Input: `{"path":"README.md"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Content, `"path": "README.md"`) || !strings.Contains(response.Content, "kaguya") {
		t.Fatalf("unexpected response: %s", response.Content)
	}
}

func TestListFilesSkipsDeniedEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "node_modules"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "pkg.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	safeFS, err := NewSafeFS(Config{RootDir: dir})
	if err != nil {
		t.Fatal(err)
	}

	listTool := findToolByName(safeFS.Tools(), "list_files")
	if listTool == nil {
		t.Fatal("list_files tool not found")
	}

	resp, err := listTool.Run(context.Background(), fantasy.ToolCall{Input: `{"path":"."}`})
	if err != nil {
		t.Fatal(err)
	}
	if resp.IsError {
		t.Fatalf("expected success, got error: %s", resp.Content)
	}
	if strings.Contains(resp.Content, "node_modules") {
		t.Fatalf("denied entry should be skipped, got: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "hello.txt") {
		t.Fatalf("normal entry should be listed, got: %s", resp.Content)
	}
}

func TestGrepOneFileLimitZero(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("match line 1\nmatch line 2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	matches, err := grepOneFile(testFile, "test.txt", "match", nil, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected 0 matches with limit=0, got %d", len(matches))
	}
}
