package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
)

type testIDGenerator struct {
	mu   sync.Mutex
	next int64
}

func (g *testIDGenerator) GenID() (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id := g.next
	g.next++
	return id, nil
}

func setup(t *testing.T) *Set {
	t.Helper()
	s, err := newSet(t.TempDir(), "test-conversation", t.TempDir(), time.Date(2026, 9, 11, 0, 0, 0, 0, time.Local), zap.NewNop())
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
	if invalid, err := newSet(t.TempDir(), "../escape", t.TempDir(), time.Now(), zap.NewNop()); err == nil {
		_ = invalid.Close()
		t.Fatal("unsafe conversation ID accepted")
	}
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
	other, err := New(s.cwd, "other-conversation", zap.NewNop())
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
	oldID := global.Id
	global.Id = &testIDGenerator{next: 123456789}
	t.Cleanup(func() { global.Id = oldID })
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
	expectedPath := filepath.Join(s.tempBase, "kaguya", "20260911", "test-conversation", "bash-123456789.log")
	if metadata.Path != expectedPath {
		t.Fatalf("unexpected output path %q", metadata.Path)
	}
	b, err := os.ReadFile(metadata.Path)
	if err != nil || !strings.HasPrefix(string(b), "line-0\n") || strings.Count(string(b), "\n") != 2100 {
		t.Fatal("full output not preserved", err)
	}
	firstPage := run(t, s.ReadTool(), ReadInput{Path: metadata.Path})
	requireOK(t, firstPage)
	if !strings.Contains(firstPage.Content, "offset=2001") {
		t.Fatal("missing output continuation", firstPage.Content)
	}
	if _, err := os.Stat(metadata.Path); err != nil {
		t.Fatal("output removed before final page", err)
	}
	offset := 2001
	lastPage := run(t, s.ReadTool(), ReadInput{Path: metadata.Path, Offset: &offset})
	requireOK(t, lastPage)
	if _, err := os.Stat(metadata.Path); err != nil {
		t.Fatal("output removed before turn ended", err)
	}
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
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(metadata.Path); err != nil {
		t.Fatal("temporary output removed by application", err)
	}
	if _, err := os.Stat(filepath.Join(s.cwd, ".kaguya")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("temporary output written into project", err)
	}
}

func TestReadRejectsOtherConversationTemporaryOutput(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	oldID := global.Id
	global.Id = &testIDGenerator{next: 200}
	t.Cleanup(func() { global.Id = oldID })

	cwd, tempBase := t.TempDir(), t.TempDir()
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.Local)
	first, err := newSet(cwd, "first", tempBase, now, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	second, err := newSet(cwd, "second", tempBase, now, zap.NewNop())
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close(); _ = second.Close() })

	r := run(t, first.BashTool(), BashInput{Command: "for ((i=0;i<2100;i++)); do echo line-$i; done"})
	requireOK(t, r)
	var metadata struct {
		Path string `json:"fullOutputPath"`
	}
	if err := json.Unmarshal([]byte(r.Metadata), &metadata); err != nil {
		t.Fatal(err)
	}
	requireOK(t, run(t, first.ReadTool(), ReadInput{Path: metadata.Path}))
	if !run(t, second.ReadTool(), ReadInput{Path: metadata.Path}).IsError {
		t.Fatal("another conversation read temporary output")
	}
	if !run(t, first.ReadTool(), ReadInput{Path: filepath.Join(tempBase, "unrelated.log")}).IsError {
		t.Fatal("read accepted unrelated temporary path")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(metadata.Path); err != nil {
		t.Fatal("temporary output removed on close", err)
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

func TestConfiguredCommandTimeout(t *testing.T) {
	s, err := New(t.TempDir(), "timeout-conversation", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetCommandTimeout(20 * time.Millisecond)
	longer := 2.0
	for _, timeout := range []*float64{nil, &longer} {
		result, err := s.bash(context.Background(), BashInput{Command: "sleep 2", Timeout: timeout})
		if err != nil || !result.IsError || !strings.Contains(result.Content, "timed out") {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
}

// 跨日期的续聊必须能读取本会话前一天已返回的输出路径，其它会话仍需拒绝。
func TestReadBashOutputAcrossDatesSameConversation(t *testing.T) {
	cwd, tempBase := t.TempDir(), t.TempDir()
	yesterday := time.Date(2026, 9, 10, 0, 0, 0, 0, time.Local)
	s, err := newSet(cwd, "cross-date", tempBase, yesterday, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	dir := filepath.Join(tempBase, s.conversationOutputDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bash-42.log")
	if err := os.WriteFile(path, []byte("line-1\nline-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 同会话（新一轮）用今天的日期创建 Set，也必须能读旧日期文件。
	today, err := newSet(cwd, "cross-date", tempBase, time.Now(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = today.Close() })
	requireOK(t, run(t, today.ReadTool(), ReadInput{Path: path}))
	if !strings.Contains(run(t, today.ReadTool(), ReadInput{Path: path}).Content, "line-1") {
		t.Fatal("cross-date output missing")
	}

	other, err := newSet(cwd, "other-conversation", tempBase, time.Now(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if !run(t, other.ReadTool(), ReadInput{Path: path}).IsError {
		t.Fatal("another conversation read cross-date output")
	}
	// 结构不符的输出路径仍然拒绝。
	bogus := filepath.Join(filepath.Dir(dir), "bash-42.log")
	if !run(t, today.ReadTool(), ReadInput{Path: bogus}).IsError {
		t.Fatal("read accepted output without conversation directory")
	}
}

// 单条命令输出达到磁盘配额时必须终止命令、保留 tail 并说明原因。
func TestBashOutputQuotaStopsCommandAndKeepsTail(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	oldID := global.Id
	global.Id = &testIDGenerator{next: 9001}
	t.Cleanup(func() { global.Id = oldID })
	cwd, tempBase := t.TempDir(), t.TempDir()
	s, err := newSet(cwd, "quota", tempBase, time.Now(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.commandOutputLimit = 64 * 1024

	start := time.Now()
	r := run(t, s.BashTool(), BashInput{Command: "yes 0123456789abcdef | head -c 2000000; sleep 30"})
	if !r.IsError && !strings.Contains(r.Content, "Output limit reached") {
		t.Fatalf("quota stop not reported: %s", r.Content)
	}
	if !strings.Contains(r.Content, "Output limit reached") {
		t.Fatalf("missing quota explanation: %s", r.Content)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("quota did not stop the command: %s", elapsed)
	}
	var metadata struct {
		Path string `json:"fullOutputPath"`
	}
	if err := json.Unmarshal([]byte(r.Metadata), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Path == "" {
		t.Fatal("saved output path missing")
	}
	info, err := os.Stat(metadata.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > s.commandOutputLimit {
		t.Fatalf("saved output exceeds quota: %d", info.Size())
	}
	if info.Size() == 0 {
		t.Fatal("saved output is empty")
	}
}

// 流式文本扫描必须保持既有的行数、末尾换行、CRLF、UTF-8 与超长行语义。
func TestReadStreamingTextBoundaries(t *testing.T) {
	s := setup(t)
	write := func(name, content string) {
		t.Helper()
		requireOK(t, run(t, s.WriteTool(), WriteInput{Path: name, Content: content}))
	}
	// 空文件是一行空内容。
	write("empty.txt", "")
	empty := run(t, s.ReadTool(), ReadInput{Path: "empty.txt"})
	requireOK(t, empty)
	if empty.Content != "" {
		t.Fatalf("empty file content=%q", empty.Content)
	}
	// 末尾换行后仍算一行；总行数包含末尾空行。
	write("trailing.txt", "a\nb\n")
	trailing := run(t, s.ReadTool(), ReadInput{Path: "trailing.txt"})
	requireOK(t, trailing)
	if !strings.Contains(trailing.Content, "a\nb") {
		t.Fatalf("trailing content=%q", trailing.Content)
	}
	// CRLF 与 UTF-8 多字节字符跨块边界保持完整。
	write("crlf.txt", "第一行\r\n第二行\r\n")
	crlf := run(t, s.ReadTool(), ReadInput{Path: "crlf.txt"})
	requireOK(t, crlf)
	if !strings.Contains(crlf.Content, "第一行\r") || !strings.Contains(crlf.Content, "第二行") {
		t.Fatalf("crlf content=%q", crlf.Content)
	}
	// 非法 UTF-8 与 NUL 字节按二进制拒绝。
	requireOK(t, run(t, s.WriteTool(), WriteInput{Path: "invalid.bin", Content: "ok\n"}))
	invalid := filepath.Join(s.cwd, "invalid.bin")
	if err := os.WriteFile(invalid, []byte{'a', 0xff, 0xfe, '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if !run(t, s.ReadTool(), ReadInput{Path: "invalid.bin"}).IsError {
		t.Fatal("invalid UTF-8 accepted")
	}
	if err := os.WriteFile(invalid, []byte{'a', 0, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if !run(t, s.ReadTool(), ReadInput{Path: "invalid.bin"}).IsError {
		t.Fatal("NUL byte accepted")
	}
	// 超长单行只给出可操作提示，不回传内容。
	long := strings.Repeat("x", MaxBytes+16)
	write("long.txt", long+"\nshort\n")
	oversized := run(t, s.ReadTool(), ReadInput{Path: "long.txt"})
	if !oversized.IsError && !strings.Contains(oversized.Content, "50KB") {
		t.Fatalf("oversized line=%+v", oversized)
	}
	// offset 越界报错并给出总行数。
	off := 99
	if !run(t, s.ReadTool(), ReadInput{Path: "trailing.txt", Offset: &off}).IsError {
		t.Fatal("accepted offset after EOF")
	}
	// 取消后的读取立即返回错误。
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ReadTool().Run(cancelled, fantasy.ToolCall{Input: `{"path":"trailing.txt"}`}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read err=%v", err)
	}
}

// 大文件的窗口读取不应把整个文件复制进内存；基准用于对比窗口大小的影响。
func BenchmarkReadToolWindow(b *testing.B) {
	for _, size := range []int{1 << 20, 8 << 20} {
		content := strings.Repeat("0123456789abcdef\n", size/17)
		cwd := b.TempDir()
		path := filepath.Join(cwd, "big.txt")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			b.Fatal(err)
		}
		set, err := newSet(cwd, "bench", b.TempDir(), time.Now(), zap.NewNop())
		if err != nil {
			b.Fatal(err)
		}
		tool := set.ReadTool()
		b.Run(fmt.Sprintf("size=%dMiB", size>>20), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				response, err := tool.Run(context.Background(), fantasy.ToolCall{Input: `{"path":"big.txt","limit":100}`})
				if err != nil || response.IsError {
					b.Fatalf("read failed: %v %+v", err, response)
				}
			}
		})
		_ = set.Close()
	}
}
