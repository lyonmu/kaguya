package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func referenceFixture(t *testing.T) (string, *os.Root, *ReferenceFilter) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		".gitignore":  "ignored/\nprivate.txt\n",
		"private.txt": "secret", "visible.txt": "visible", ".hidden.txt": "hidden",
		"ignored/child.txt": "secret", ".git/config": "secret", "dist/out.txt": "generated",
		"sub/.gitignore": "nested.txt\n", "sub/nested.txt": "secret", "sub/safe.txt": "safe",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	matcher, err := NewIgnoreMatcher(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return dir, root, NewReferenceFilter(context.Background(), root, matcher)
}

func TestReferenceFilterExplicitAndAutomaticPolicy(t *testing.T) {
	_, _, filter := referenceFixture(t)
	for _, name := range []string{"private.txt", "ignored/child.txt", ".git/config", "dist/out.txt", "sub/nested.txt", "../outside", "/outside"} {
		if _, err := filter.Check(name, "file", false); !errors.Is(err, ErrReferenceFiltered) {
			t.Errorf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"ignored", ".git", "dist"} {
		if _, err := filter.Check(name, "directory", false); !errors.Is(err, ErrReferenceFiltered) {
			t.Errorf("directory %q: %v", name, err)
		}
	}
	for _, name := range []string{"visible.txt", ".hidden.txt", "sub/safe.txt"} {
		file, proof, err := filter.OpenFile(name, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := filter.Verify(proof, file); err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	if _, err := filter.Check(".hidden.txt", "file", true); !errors.Is(err, ErrReferenceFiltered) {
		t.Fatal(err)
	}
	if _, err := filter.Check("visible.txt", "directory", false); !errors.Is(err, ErrReferenceType) {
		t.Fatal(err)
	}
	if _, err := filter.Check(".", "directory", true); err != nil {
		t.Fatal(err)
	}
}

func TestReferenceFilterSymlinkTargetsAndAncestors(t *testing.T) {
	dir, _, filter := referenceFixture(t)
	links := map[string]string{
		"alias.txt": "sub/safe.txt", "private-alias": "private.txt", "git-alias": ".git/config",
		"nested-alias": "sub/nested.txt", "dist-alias": "dist/out.txt", "escape": "../outside.txt",
		"absolute": filepath.Join(dir, "visible.txt"), "loop-a": "loop-b", "loop-b": "loop-a", "dir-alias": "sub",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	file, proof, err := filter.OpenFile("alias.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if proof.Path != "sub/safe.txt" {
		t.Fatal(proof.Path)
	}
	for _, name := range []string{"private-alias", "git-alias", "nested-alias", "dist-alias", "escape", "absolute", "loop-a"} {
		if _, err := filter.Check(name, "file", false); !errors.Is(err, ErrReferenceFiltered) {
			t.Errorf("%q: %v", name, err)
		}
	}
	if _, err := filter.Check("alias.txt", "file", true); !errors.Is(err, ErrReferenceFiltered) {
		t.Fatal(err)
	}
	if _, err := filter.Check("dir-alias", "directory", false); !errors.Is(err, ErrReferenceFiltered) {
		t.Fatal(err)
	}
	if _, err := filter.Check("dir-alias/child", "directory", false); !errors.Is(err, ErrReferenceFiltered) {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("visible.txt", filepath.Join(dir, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	if err := filter.Verify(proof, file); !errors.Is(err, ErrReferenceChanged) {
		t.Fatal(err)
	}
}

func TestReferenceHandleReadDoesNotReopenReplacedPath(t *testing.T) {
	dir, _, filter := referenceFixture(t)
	file, proof, err := filter.OpenFile("visible.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := os.WriteFile(filepath.Join(dir, "replacement.txt"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "replacement.txt"), filepath.Join(dir, "visible.txt")); err != nil {
		t.Fatal(err)
	}
	window, err := ReadTextWindowFile(context.Background(), file, 0, 10, 1024, 1024, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(window.Content) != "visible" {
		t.Fatalf("read reopened replacement: %q", window.Content)
	}
	if err := filter.Verify(proof, file); !errors.Is(err, ErrReferenceChanged) {
		t.Fatal(err)
	}
}

func TestReferenceFilterStrictIgnoreFailureAndCancellation(t *testing.T) {
	dir, root, _ := referenceFixture(t)
	if err := os.Remove(filepath.Join(dir, "sub/.gitignore")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub/.gitignore"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	matcher, err := NewIgnoreMatcher(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	filter := NewReferenceFilter(ctx, root, matcher)
	if _, err := filter.Check("sub/safe.txt", "file", false); !errors.Is(err, ErrReferenceIgnore) {
		t.Fatal(err)
	}
	cancel()
	if _, _, err := filter.OpenFile("visible.txt", false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := ReadTextWindowFile(ctx, nil, 0, 1, 1, 1, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
