//go:build darwin

package desktop

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseLoginShellPath(t *testing.T) {
	output := []byte("nvm output\nPATH=/opt/homebrew/bin:/usr/bin\nTERM=xterm\n")
	if got := parseLoginShellPath(output); got != "/opt/homebrew/bin:/usr/bin" {
		t.Fatalf("parsed PATH=%q", got)
	}
	if got := parseLoginShellPath([]byte("no path here\n")); got != "" {
		t.Fatalf("unexpected PATH=%q", got)
	}
	if got := parseLoginShellPath([]byte("PATH=/a\r\n")); got != "/a" {
		t.Fatalf("CRLF PATH=%q", got)
	}
}

func TestMergePathKeepsLoginEntriesFirst(t *testing.T) {
	got := mergePath("/opt/homebrew/bin:/usr/bin", "/usr/bin:/bin")
	if got != "/opt/homebrew/bin:/usr/bin:/bin" {
		t.Fatalf("merged PATH=%q", got)
	}
	if got := mergePath("", "/usr/bin:/bin"); got != "/usr/bin:/bin" {
		t.Fatalf("empty login PATH=%q", got)
	}
	if got := mergePath("/a::/a", ""); got != "/a" {
		t.Fatalf("deduplicated PATH=%q", got)
	}
}

// 真实 shell 冒烟测试：交互式登录 shell 输出中必须能解析到 PATH。
func TestReadLoginShellPathSmoke(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	path, err := readLoginShellPath(ctx, "/bin/sh")
	if err != nil || !strings.Contains(path, "/") {
		t.Fatalf("login shell PATH=%q err=%v", path, err)
	}
}
