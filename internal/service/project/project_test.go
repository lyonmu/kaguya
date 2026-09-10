package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
)

func TestDirectoryBoundary(t *testing.T) {
	home, homeErr := filepath.EvalSymlinks(t.TempDir())
	if homeErr != nil {
		t.Fatal(homeErr)
	}
	outside := t.TempDir()
	t.Setenv("HOME", home)
	previous := global.Logger
	global.Logger = zap.NewNop()
	t.Cleanup(func() { global.Logger = previous })
	child := filepath.Join(home, "project")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "file"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"escape": outside, "inside": child, "broken": filepath.Join(home, "missing")} {
		if err := os.Symlink(target, filepath.Join(home, name)); err != nil {
			t.Fatal(err)
		}
	}
	svc := &ProjectSvc{}
	root, err := svc.Directories(context.Background(), "")
	if err != nil || root.Path != home || root.Parent != "" || len(root.Items) != 1 || root.Items[0].Path != child {
		t.Fatalf("root=%+v err=%v", root, err)
	}
	result, err := svc.Directories(context.Background(), child)
	if err != nil || result.Parent != home || len(result.Items) != 0 {
		t.Fatalf("child=%+v err=%v", result, err)
	}
	for _, path := range []string{outside, filepath.Join(home, "escape"), filepath.Join(home, "file"), filepath.Dir(home), "../", "relative", home + "-other"} {
		if _, err := svc.Directories(context.Background(), path); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	_, canonical, err := resolveDirectory(filepath.Join(home, "inside"))
	if err != nil || canonical != child {
		t.Fatalf("canonical=%s err=%v", canonical, err)
	}
	// The root handle also enforces containment independently of pre-validation.
	if _, err := openDirectory(home, filepath.Join(home, "escape")); err == nil {
		t.Fatal("opened escape link")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.Directories(ctx, home); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestHomeSymlinkAcceptsOwnAliasButRejectsEscape(t *testing.T) {
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", alias)
	if err := os.Mkdir(filepath.Join(real, "project"), 0700); err != nil {
		t.Fatal(err)
	}
	_, got, err := resolveDirectory(filepath.Join(alias, "project"))
	if err != nil || got != filepath.Join(real, "project") {
		t.Fatalf("resolved=%q err=%v", got, err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(real, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveDirectory(filepath.Join(alias, "escape")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("escape=%v", err)
	}
}
