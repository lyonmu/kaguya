package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 导入资料读取复用项目路径校验、忽略规则、二进制探测与大小限制。
func TestReadDocumentSafety(t *testing.T) {
	svc, _, dir := setupGitTest(t)
	id := createProject(t, svc, dir)
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("docs/spec.md", "项目规范内容")
	write(".gitignore", "ignored.txt\n")
	write("ignored.txt", "不应导入")
	write("binary.bin", "head\x00tail")
	write("large.txt", strings.Repeat("a", 1024))
	ctx := context.Background()

	doc, err := svc.ReadDocument(ctx, id, "docs/spec.md", 4096)
	if err != nil || doc.Path != "docs/spec.md" || doc.Content != "项目规范内容" || doc.Size != int64(len("项目规范内容")) {
		t.Fatalf("doc=%+v err=%v", doc, err)
	}
	for name, path := range map[string]string{
		"越级":  "../outside.txt",
		"绝对":  "/etc/passwd",
		"空路径": "",
		"忽略":  "ignored.txt",
		"二进制": "binary.bin",
	} {
		if _, err := svc.ReadDocument(ctx, id, path, 4096); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: err=%v", name, err)
		}
	}
	if _, err := svc.ReadDocument(ctx, id, "large.txt", 16); !errors.Is(err, ErrInvalid) {
		t.Fatalf("too large err=%v", err)
	}
	// 缺失项目明确失败。
	if _, err := svc.ReadDocument(ctx, "missing", "docs/spec.md", 4096); err == nil {
		t.Fatal("missing project must fail")
	}
}
