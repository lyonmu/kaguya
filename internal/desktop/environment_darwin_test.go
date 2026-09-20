//go:build darwin

package desktop

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseLoginShellEnvironment(t *testing.T) {
	output := []byte("PATH=/opt/homebrew/bin:/usr/bin\x00GOPATH=/workspace/go\x00HTTP_PROXY=http://proxy.example\x00MULTILINE=first\nsecond\x00EMPTY=\x00")
	got := parseLoginShellEnvironment(output)
	if got["PATH"] != "/opt/homebrew/bin:/usr/bin" {
		t.Fatalf("parsed PATH=%q", got["PATH"])
	}
	if got["GOPATH"] != "/workspace/go" {
		t.Fatalf("parsed GOPATH=%q", got["GOPATH"])
	}
	if got["HTTP_PROXY"] != "http://proxy.example" {
		t.Fatalf("parsed HTTP_PROXY=%q", got["HTTP_PROXY"])
	}
	if got["MULTILINE"] != "first\nsecond" || got["EMPTY"] != "" {
		t.Fatalf("parsed environment=%v", got)
	}
}

func TestAdoptLoginShellEnvironment(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("GOPATH", "/old/go")
	t.Setenv("HTTP_PROXY", "")

	adoptLoginShellEnvironment(map[string]string{
		"PATH":       "/opt/homebrew/bin:/usr/bin",
		"GOPATH":     "/workspace/go",
		"HTTP_PROXY": "http://proxy.example",
	})

	if got := os.Getenv("PATH"); got != "/opt/homebrew/bin:/usr/bin" {
		t.Fatalf("adopted PATH=%q", got)
	}
	if got := os.Getenv("GOPATH"); got != "/workspace/go" {
		t.Fatalf("adopted GOPATH=%q", got)
	}
	if got := os.Getenv("HTTP_PROXY"); got != "http://proxy.example" {
		t.Fatalf("adopted HTTP_PROXY=%q", got)
	}
}

func TestReadLoginShellEnvironmentSourcesZshrc(t *testing.T) {
	if _, err := os.Stat("/bin/zsh"); err != nil {
		t.Skip("no /bin/zsh")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("export KAGUYA_ENV_TEST='loaded from zshrc'\nexport GOPATH='/workspace/go'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	environment, err := readLoginShellEnvironment(ctx, "/bin/zsh")
	if err != nil {
		t.Fatal(err)
	}
	if environment["KAGUYA_ENV_TEST"] != "loaded from zshrc" || environment["GOPATH"] != "/workspace/go" {
		t.Fatalf("login shell environment=%v", environment)
	}
}
