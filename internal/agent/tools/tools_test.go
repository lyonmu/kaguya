package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/fantasy"
	"go.uber.org/zap"
)

func setup(t *testing.T) *Set {
	t.Helper()
	s, err := New(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func run(t *testing.T, tool fantasy.AgentTool, input any) fantasy.ToolResponse {
	t.Helper()
	b, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r, err := tool.Run(context.Background(), fantasy.ToolCall{ID: "test", Name: tool.Info().Name, Input: string(b)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func requireOK(t *testing.T, r fantasy.ToolResponse) {
	t.Helper()
	if r.IsError {
		t.Fatal(r.Content)
	}
}
func TestToolSetAndValidation(t *testing.T) {
	s := setup(t)
	names := []string{}
	for _, tool := range s.AllTools() {
		names = append(names, tool.Info().Name)
		if tool.Info().Parallel {
			t.Fatal("mutations must be sequential")
		}
	}
	if !reflect.DeepEqual(names, []string{"read", "bash", "edit", "write", "grep", "find", "ls"}) {
		t.Fatal(names)
	}
	if len(s.CodingTools()) != 4 || len(s.ReadOnlyTools()) != 4 {
		t.Fatal("wrong default toolset")
	}
	r := run(t, s.WriteTool(), map[string]any{"path": "must-not-exist"})
	if !r.IsError {
		t.Fatal("missing content accepted")
	}
	if _, err := os.Stat(filepath.Join(s.cwd, "must-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid input mutated file")
	}
	if !strings.Contains(s.SystemPrompt(), s.cwd) {
		t.Fatal("prompt missing cwd")
	}
}
func TestReadWriteBoundariesAndPagination(t *testing.T) {
	s := setup(t)
	requireOK(t, run(t, s.WriteTool(), WriteInput{Path: "nested/file.txt", Content: "一\n二\n三\n"}))
	off, limit := 2, 1
	r := run(t, s.ReadTool(), ReadInput{Path: filepath.Join(s.cwd, "nested/file.txt"), Offset: &off, Limit: &limit})
	requireOK(t, r)
	if !strings.HasPrefix(r.Content, "二\n") || !strings.Contains(r.Content, "offset=3") {
		t.Fatal(r.Content)
	}
	off = 99
	if !run(t, s.ReadTool(), ReadInput{Path: "nested/file.txt", Offset: &off}).IsError {
		t.Fatal("accepted offset after EOF")
	}
	if !run(t, s.ReadTool(), ReadInput{Path: "nested"}).IsError {
		t.Fatal("accepted directory")
	}
	requireOK(t, run(t, s.WriteTool(), WriteInput{Path: "empty", Content: ""}))
	requireOK(t, run(t, s.ReadTool(), ReadInput{Path: "empty"}))
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600)
	if err := os.Symlink(outside, filepath.Join(s.cwd, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret", filepath.Join(outside, "secret"), "escape/secret"} {
		if !run(t, s.ReadTool(), ReadInput{Path: path}).IsError {
			t.Fatalf("read escape %s", path)
		}
		if !run(t, s.WriteTool(), WriteInput{Path: path, Content: "bad"}).IsError {
			t.Fatalf("write escape %s", path)
		}
	}
	b, _ := os.ReadFile(filepath.Join(outside, "secret"))
	if string(b) != "secret" {
		t.Fatal("changed outside workspace")
	}
	t.Setenv("HOME", s.cwd)
	requireOK(t, run(t, s.ReadTool(), ReadInput{Path: "@~/nested/file.txt"}))
}
func TestEditAtomicityAndNormalization(t *testing.T) {
	s := setup(t)
	path := filepath.Join(s.cwd, "file")
	raw := "\ufeffalpha\r\nbeta\r\ngamma\r\n"
	if err := os.WriteFile(path, []byte(raw), 0755); err != nil {
		t.Fatal(err)
	}
	r := run(t, s.EditTool(), EditInput{Path: "file", Edits: []Replacement{{"alpha", "one"}, {"gamma", "three"}}})
	requireOK(t, r)
	b, _ := os.ReadFile(path)
	if string(b) != "\ufeffone\r\nbeta\r\nthree\r\n" {
		t.Fatalf("%q", b)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0755 {
		t.Fatal("mode changed")
	}
	if !strings.Contains(r.Metadata, "firstChangedLine") || !strings.Contains(r.Metadata, "patch") {
		t.Fatal("missing diff metadata")
	}
	before := string(b)
	for _, edits := range [][]Replacement{{{"one", "first"}, {"missing", "x"}}, {{"one", "x"}, {"one\nbeta", "y"}}, {{"one", "first"}, {"first", "last"}}, {{"", "x"}}, {{"one", "one"}}} {
		r := run(t, s.EditTool(), EditInput{Path: "file", Edits: edits})
		if !r.IsError {
			t.Fatal("invalid edit accepted", edits)
		}
		b, _ := os.ReadFile(path)
		if string(b) != before {
			t.Fatal("failed batch partially wrote file")
		}
	}
	os.WriteFile(path, []byte("duplicate duplicate"), 0644)
	if !run(t, s.EditTool(), EditInput{Path: "file", Edits: []Replacement{{"duplicate", "x"}}}).IsError {
		t.Fatal("ambiguous edit accepted")
	}
	original := "keep “quotes”  \nchange “this”  \nkeep — dash  \n"
	updated, _, err := applyEdits(original, []Replacement{{"change \"this\"", "new"}})
	if err != nil || updated != "keep “quotes”  \nnew\nkeep — dash  \n" {
		t.Fatalf("fuzzy=%q err=%v", updated, err)
	}
}
func TestMutationSerializationAndCancellation(t *testing.T) {
	s := setup(t)
	other, err := New(s.cwd, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	requireOK(t, run(t, s.WriteTool(), WriteInput{Path: "file", Content: "left right"}))
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, set := range []*Set{s, other} {
		wg.Add(1)
		go func(i int, set *Set) {
			defer wg.Done()
			old, new := "left", "LEFT"
			if i == 1 {
				old, new = "right", "RIGHT"
			}
			_, e := set.edit(context.Background(), EditInput{Path: "file", Edits: []Replacement{{old, new}}})
			errs <- e
		}(i, set)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(s.cwd, "file"))
	if string(b) != "LEFT RIGHT" {
		t.Fatal(string(b))
	}
	release, err := lockPath(context.Background(), filepath.Join(s.cwd, "file"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := other.write(ctx, WriteInput{Path: "file", Content: "bad"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestUnifiedPatchEmptyFile(t *testing.T) {
	if patch := unifiedPatch("file", "line\n", ""); !strings.Contains(patch, "+0,0") {
		t.Fatal(patch)
	}
	if patch := unifiedPatch("file", "", "line\n"); !strings.Contains(patch, "-0,0") {
		t.Fatal(patch)
	}
}

func TestReadImageAttachment(t *testing.T) {
	s := setup(t)
	f, err := os.Create(filepath.Join(s.cwd, "image.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewNRGBA(image.Rect(0, 0, 2400, 10))); err != nil {
		t.Fatal(err)
	}
	f.Close()
	r := run(t, s.ReadTool(), ReadInput{Path: "image.png"})
	requireOK(t, r)
	if r.Type != "image" || r.MediaType != "image/png" || len(r.Data) == 0 {
		t.Fatal("image not attached")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(r.Data))
	if err != nil || cfg.Width != 2000 || cfg.Height > 2000 {
		t.Fatalf("image=%+v err=%v", cfg, err)
	}
}

func TestTruncation(t *testing.T) {
	for _, text := range []string{strings.Repeat("a\n", MaxLines+20), strings.Repeat("中", MaxBytes), strings.Repeat("line\n", 20000)} {
		for _, tail := range []bool{false, true} {
			r := truncate(text, MaxLines, tail)
			if !r.Truncated || len(r.Content) > MaxBytes || r.OutputLines > MaxLines || !utf8.ValidString(r.Content) {
				t.Fatalf("bad truncation: %+v", r)
			}
		}
	}
	s := setup(t)
	requireOK(t, run(t, s.WriteTool(), WriteInput{Path: "large", Content: strings.Repeat("a\n", MaxLines+20)}))
	r := run(t, s.ReadTool(), ReadInput{Path: "large"})
	requireOK(t, r)
	if !strings.Contains(r.Content, "offset=2001") {
		t.Fatal("missing continuation", r.Content)
	}
}
func TestBashCWDOutputExitAndTimeout(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	s := setup(t)
	r := run(t, s.BashTool(), BashInput{Command: "pwd; printf error >&2"})
	requireOK(t, r)
	if !strings.Contains(r.Content, s.cwd) || !strings.Contains(r.Content, "error") {
		t.Fatal(r.Content)
	}
	r = run(t, s.BashTool(), BashInput{Command: "echo partial; exit 7"})
	if !r.IsError || !strings.Contains(r.Content, "partial") || !strings.Contains(r.Content, "7") {
		t.Fatal(r)
	}
	r = run(t, s.BashTool(), BashInput{Command: "for ((i=0;i<2100;i++)); do echo line-$i; done"})
	requireOK(t, r)
	if !strings.Contains(r.Content, "line-2099") || !strings.Contains(r.Content, "Full output:") {
		t.Fatal("tail missing")
	}
	var metadata struct {
		Path string `json:"fullOutputPath"`
	}
	if err := json.Unmarshal([]byte(r.Metadata), &metadata); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(metadata.Path)
	if err != nil || !strings.HasPrefix(string(b), "line-0\n") || strings.Count(string(b), "\n") != 2100 {
		t.Fatal("full output not preserved", err)
	}
	requireOK(t, run(t, s.ReadTool(), ReadInput{Path: metadata.Path}))
	timeout := 0.05
	start := time.Now()
	r = run(t, s.BashTool(), BashInput{Command: "echo before; (sleep 0.3; touch leaked-child) & wait", Timeout: &timeout})
	if !r.IsError || !strings.Contains(r.Content, "timed out") || !strings.Contains(r.Content, "before") || time.Since(start) > 3*time.Second {
		t.Fatal(r)
	}
	time.Sleep(350 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(s.cwd, "leaked-child")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("timeout left child process running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err = s.BashTool().Run(ctx, fantasy.ToolCall{Input: `{"command":"sleep 10 & wait"}`})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestSearchTools(t *testing.T) {
	s := setup(t)
	requireOK(t, run(t, s.WriteTool(), WriteInput{Path: ".gitignore", Content: "ignored.txt\n"}))
	for p, text := range map[string]string{"a.go": "Alpha\nbeta\n", "dir/b.go": "alpha\n", "ignored.txt": "alpha"} {
		requireOK(t, run(t, s.WriteTool(), WriteInput{Path: p, Content: text}))
	}
	r := run(t, s.LsTool(), LsInput{})
	requireOK(t, r)
	if !strings.Contains(r.Content, "dir/") || !strings.Contains(r.Content, ".gitignore") {
		t.Fatal(r)
	}
	if _, err := exec.LookPath("rg"); err == nil {
		// ripgrep follows gitignore rules inside repositories.
		if err := os.Mkdir(filepath.Join(s.cwd, ".git"), 0755); err != nil {
			t.Fatal(err)
		}
		r = run(t, s.GrepTool(), GrepInput{Pattern: "alpha", IgnoreCase: true, Glob: "*.go", Context: 1})
		requireOK(t, r)
		if !strings.Contains(r.Content, "a.go:1: Alpha") || !strings.Contains(r.Content, "a.go-2- beta") || strings.Contains(r.Content, "ignored.txt") {
			t.Fatal(r.Content)
		}
		r = run(t, s.GrepTool(), GrepInput{Pattern: "alpha", IgnoreCase: true})
		requireOK(t, r)
		if strings.Contains(r.Content, "ignored.txt") {
			t.Fatal("grep ignored .gitignore")
		}
		r = run(t, s.GrepTool(), GrepInput{Pattern: "["})
		if !r.IsError {
			t.Fatal("invalid regex accepted")
		}
	}
	if _, err := exec.LookPath("fd"); err == nil {
		r = run(t, s.FindTool(), FindInput{Pattern: "*.go"})
		requireOK(t, r)
		if !strings.Contains(r.Content, "a.go") || !strings.Contains(r.Content, "dir/b.go") {
			t.Fatal(r.Content)
		}
	}
}
