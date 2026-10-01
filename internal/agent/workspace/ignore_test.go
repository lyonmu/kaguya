package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestIgnoreMatcher 在临时项目根写入 .gitignore 后返回宽松匹配器。
func newTestIgnoreMatcher(t *testing.T, lines ...string) *IgnoreMatcher {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(strings.Join(lines, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return NewLenientIgnoreMatcher(root)
}

func TestIgnoreRuleMatching(t *testing.T) {
	matcher := newTestIgnoreMatcher(t,
		"# comment",
		"build/",
		"*.log",
		"!keep.log",
		"/root-only.txt",
		"docs/**/draft.md",
		"tmp?.txt",
		"[ab].go",
		`\#literal`,
	)
	cases := []struct {
		path string
		dir  bool
		want bool
	}{
		{path: "build", dir: true, want: true},
		{path: "a/build", dir: true, want: true},
		{path: "build", dir: false, want: false},
		{path: "a/b/x.log", want: true},
		{path: "keep.log", want: false},
		{path: "a/keep.log", want: false},
		{path: "root-only.txt", want: true},
		{path: "a/root-only.txt", want: false},
		{path: "docs/draft.md", want: true},
		{path: "docs/a/b/draft.md", want: true},
		{path: "other/draft.md", want: false},
		{path: "tmp1.txt", want: true},
		{path: "tmp12.txt", want: false},
		{path: "a.go", want: true},
		{path: "b.go", want: true},
		{path: "c.go", want: false},
		{path: "#literal", want: true},
	}
	for _, item := range cases {
		if got := matcher.Ignored(item.path, item.dir); got != item.want {
			t.Errorf("Ignored(%q, dir=%t)=%t want %t", item.path, item.dir, got, item.want)
		}
	}
}

func TestIgnoreRuleNestedBase(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", ".gitignore"), []byte("dist/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	matcher := NewLenientIgnoreMatcher(root)
	if !matcher.Ignored("web/dist", true) {
		t.Fatal("web/dist should be ignored")
	}
	if matcher.Ignored("dist", true) || matcher.Ignored("web/dist", false) {
		t.Fatal("nested base should not leak")
	}
	// 子目录规则只对自身子树生效。
	if matcher.Ignored("lib/dist", true) || matcher.Ignored("web2/dist", true) {
		t.Fatal("nested rules applied outside their directory")
	}
	if matcher.Ignored("web/other", true) {
		t.Fatal("unrelated path ignored")
	}
}

func TestStrictIgnoreMatcherReportsUnreadableNestedRules(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0700); err != nil {
		t.Fatal(err)
	}
	// 目录冒充 .gitignore：读取一定失败，严格模式必须报错而不是放行内容。
	if err := os.MkdirAll(filepath.Join(dir, "web", ".gitignore"), 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	matcher, err := NewIgnoreMatcher(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := matcher.Match("web/main.go", false); err == nil {
		t.Fatal("strict matcher silently ignored an unreadable ignore file")
	}
	// 宽松模式保持 UI 文件树的默认语义：读不到就按空规则。
	if NewLenientIgnoreMatcher(root).Ignored("web/main.go", false) {
		t.Fatal("lenient matcher must not fail closed")
	}
}
