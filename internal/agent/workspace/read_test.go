package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func newRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}

func TestCleanRelative(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
		ok    bool
	}{
		{"src/main.go", "src/main.go", true},
		{"a/../b.txt", "b.txt", true},
		{".", ".", true},
		{"", "", false},
		{"/etc/passwd", "", false},
		{"../secret", "", false},
		{"a/../../b", "", false},
		{string([]byte{'a', 0, 'b'}), "", false},
		{strings.Repeat("a", 5000), "", false},
	} {
		got, err := CleanRelative(tc.input, 4096)
		if tc.ok && (err != nil || got != tc.want) {
			t.Errorf("CleanRelative(%q)=(%q,%v), want %q", tc.input, got, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Errorf("CleanRelative(%q) accepted invalid path", tc.input)
		}
	}
}

func TestOpenRegularAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "inside.txt"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	root := newRoot(t, dir)

	file, info, err := OpenRegular(root, "inside.txt")
	if err != nil || info.Size() != 2 {
		t.Fatalf("open inside: %v", err)
	}
	file.Close()

	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenRegular(root, "escape.txt"); err == nil {
		t.Fatal("symlink escaping the workspace was followed")
	}
	// 目录不是 regular file。
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenRegular(root, "sub"); err == nil {
		t.Fatal("directory accepted as regular file")
	}
}

func TestReadTextWindowKeepsLineSemantics(t *testing.T) {
	dir := t.TempDir()
	content := "第一行🌙\nsecond\n\nfourth"
	if err := os.WriteFile(filepath.Join(dir, "text.txt"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	root := newRoot(t, dir)
	window, err := ReadTextWindow(context.Background(), root, "text.txt", 0, 2000, 1<<20, 50*1024, 50*1024)
	if err != nil {
		t.Fatal(err)
	}
	if window.Binary || window.Grown || window.Lines != 4 {
		t.Fatalf("window=%+v", window)
	}
	if string(window.Content) != content {
		t.Fatalf("content=%q", window.Content)
	}
	// 空文件按 1 行处理。
	if err := os.WriteFile(filepath.Join(dir, "empty.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	empty, err := ReadTextWindow(context.Background(), root, "empty.txt", 0, 2000, 1<<20, 50*1024, 50*1024)
	if err != nil || empty.Lines != 1 || len(empty.Content) != 0 {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
}

func TestReadTextWindowDetectsBinaryAndOversizedLines(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nul.bin"), []byte("abc\x00def"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "invalid.txt"), []byte{'a', 0xff, 'b'}, 0600); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", 64)
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(long+"\nshort\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root := newRoot(t, dir)
	for name, wantBinary := range map[string]bool{"nul.bin": true, "invalid.txt": true} {
		window, err := ReadTextWindow(context.Background(), root, name, 0, 2000, 1<<20, 50*1024, 50*1024)
		if err != nil || !window.Binary || !wantBinary {
			t.Fatalf("%s: window=%+v err=%v", name, window, err)
		}
	}
	window, err := ReadTextWindow(context.Background(), root, "long.txt", 0, 2000, 1<<20, 16, 16)
	if err != nil {
		t.Fatal(err)
	}
	if !window.OversizedLine || !window.FirstLine {
		t.Fatalf("oversized line not reported: %+v", window)
	}
}

func TestReadTextWindowGrownFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Repeat("a", 1024)), 0600); err != nil {
		t.Fatal(err)
	}
	root := newRoot(t, dir)
	window, err := ReadTextWindow(context.Background(), root, "big.txt", 0, 2000, 100, 50*1024, 50*1024)
	if err != nil {
		t.Fatal(err)
	}
	if !window.Grown {
		t.Fatalf("grown file not reported: %+v", window)
	}
}

func TestReadTextWindowBoundsTotalBytesAndResumesWithoutSkipping(t *testing.T) {
	dir := t.TempDir()
	line := strings.Repeat("月", 33)
	content := strings.Repeat(line+"\n", 1000)
	if err := os.WriteFile(filepath.Join(dir, "text"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	root := newRoot(t, dir)
	const outputLimit = 50 * 1024
	first, err := ReadTextWindow(context.Background(), root, "text", 0, 2000, 1<<20, outputLimit, outputLimit)
	if err != nil || !first.Truncated || len(first.Content) > outputLimit || !utf8.Valid(first.Content) || first.Lines != 1000 || first.NextLine != first.OutputLines+1 {
		t.Fatalf("invalid bounded first window: %+v err=%v", first, err)
	}
	second, err := ReadTextWindow(context.Background(), root, "text", first.NextLine-1, 2000, 1<<20, outputLimit, outputLimit)
	if err != nil || second.Truncated || second.NextLine != 0 || first.OutputLines+second.OutputLines != 1000 {
		t.Fatalf("invalid continuation: lines=%d next=%d err=%v", second.OutputLines, second.NextLine, err)
	}
	if string(first.Content)+"\n"+string(second.Content)+"\n" != content {
		t.Fatal("bounded windows skipped, duplicated or split a line")
	}
}

func TestReadTextWindowKeepsEmptyLinesAndIndependentLimits(t *testing.T) {
	for _, tc := range []struct {
		name, text, want         string
		offset, limit            int
		lineBytes, outputBytes   int64
		lines, outputLines, next int
		truncated, oversized     bool
	}{
		{"empty", "", "", 0, 10, 5, 0, 1, 1, 0, false, false},
		{"empty lines", "\n\nlast\n", "\n\nlast", 0, 10, 5, 10, 3, 3, 0, false, false},
		{"empty offset", "\n\nlast\n", "", 1, 1, 5, 10, 3, 1, 3, false, false},
		{"separator", "a\nb\n", "a", 0, 10, 5, 2, 2, 1, 2, true, false},
		{"exact", "a\nb\n", "a\nb", 0, 10, 5, 3, 2, 2, 0, false, false},
		{"zero", "a\n", "", 0, 10, 5, 0, 1, 0, 1, true, false},
		{"long first", "abcdef\nshort", "", 0, 10, 5, 30, 2, 0, 1, true, true},
		{"long second", "ok\nabcdef\nshort", "ok", 0, 10, 5, 30, 3, 1, 2, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "text"), []byte(tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadTextWindow(context.Background(), newRoot(t, dir), "text", tc.offset, tc.limit, 1<<20, tc.lineBytes, tc.outputBytes)
			if err != nil || string(got.Content) != tc.want || got.Lines != tc.lines || got.OutputLines != tc.outputLines || got.NextLine != tc.next || got.Truncated != tc.truncated || got.OversizedLine != tc.oversized {
				t.Fatalf("window=%+v err=%v", got, err)
			}
		})
	}
}

func TestReadTextWindowDoesNotCacheOversizedOrSkippedLines(t *testing.T) {
	dir := t.TempDir()
	file, err := os.Create(filepath.Join(dir, "text"))
	if err != nil {
		t.Fatal(err)
	}
	chunk := []byte(strings.Repeat("x", 64<<10))
	for range 256 { // 16MiB，无换行；夹具创建不计入读取内存。
		if _, err := file.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := file.WriteString("\nnext🌙\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	root := newRoot(t, dir)
	for _, offset := range []int{0, 1} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got, err := ReadTextWindow(context.Background(), root, "text", offset, 10, 32<<20, 1024, 1024)
		runtime.ReadMemStats(&after)
		if err != nil || got.Lines != 2 || got.Binary {
			t.Fatalf("long line scan: %+v err=%v", got, err)
		}
		if offset == 0 && (!got.OversizedLine || got.OutputLines != 0 || got.NextLine != 1) {
			t.Fatal("oversized first line was silently skipped")
		}
		if offset == 1 && (string(got.Content) != "next🌙" || got.NextLine != 0) {
			t.Fatal("skip offset lost the bounded next line")
		}
		if used := after.TotalAlloc - before.TotalAlloc; used > 8<<20 {
			t.Fatalf("16MiB line caused unbounded buffering: allocated=%d", used)
		}
	}
}

func TestReadTextWindowRejectsCancellationAndInvalidPolicy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := newRoot(t, t.TempDir())
	if _, err := ReadTextWindow(ctx, root, "missing", 0, 1, 1024, 10, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if _, err := ReadTextWindow(context.Background(), root, "missing", -1, 1, 1024, 10, 10); err == nil || os.IsNotExist(err) {
		t.Fatalf("invalid policy should fail before opening: %v", err)
	}
}
