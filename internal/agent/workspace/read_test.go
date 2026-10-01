package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	window, err := ReadTextWindow(context.Background(), root, "text.txt", 0, 2000, 1<<20, 50*1024)
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
	empty, err := ReadTextWindow(context.Background(), root, "empty.txt", 0, 2000, 1<<20, 50*1024)
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
		window, err := ReadTextWindow(context.Background(), root, name, 0, 2000, 1<<20, 50*1024)
		if err != nil || !window.Binary || !wantBinary {
			t.Fatalf("%s: window=%+v err=%v", name, window, err)
		}
	}
	window, err := ReadTextWindow(context.Background(), root, "long.txt", 0, 2000, 1<<20, 16)
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
	window, err := ReadTextWindow(context.Background(), root, "big.txt", 0, 2000, 100, 50*1024)
	if err != nil {
		t.Fatal(err)
	}
	if !window.Grown {
		t.Fatalf("grown file not reported: %+v", window)
	}
}
