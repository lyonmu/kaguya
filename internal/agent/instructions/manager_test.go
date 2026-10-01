package instructions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func testOptions() Options {
	return Options{Debounce: 5 * time.Millisecond, Settle: 5 * time.Millisecond, Reconcile: 50 * time.Millisecond, Logger: zap.NewNop()}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 全局指令不再有数量与总字节上限：超过旧 32 路径 / 256KiB 也必须成功。
func TestLoadHasNoCountOrSizeLimit(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for index := 0; index < 40; index++ {
		path := filepath.Join(dir, "instructions-"+string(rune('a'+index%26))+string(rune('0'+index/26))+".md")
		writeFile(t, path, "content-"+string(rune('a'+index%26)))
		paths = append(paths, path)
	}
	large := filepath.Join(dir, "large.md")
	writeFile(t, large, strings.Repeat("大", 200*1024))
	paths = append(paths, large)
	snapshot, _, err := Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Sources) != len(paths) {
		t.Fatalf("sources=%d paths=%d", len(snapshot.Sources), len(paths))
	}
	if len(snapshot.Text) <= 256*1024 {
		t.Fatalf("large global text was truncated: %d bytes", len(snapshot.Text))
	}
	if !strings.Contains(snapshot.Text, "content-a") {
		t.Fatal("missing configured content")
	}
}

func TestLoadDedupesSameFileAndMissingPaths(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "notes.md")
	writeFile(t, real, "shared")
	alias := filepath.Join(dir, "alias.md")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := Load([]string{real, alias, filepath.Join(dir, "missing.md")})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Sources) != 1 || snapshot.Sources[0].Path != real {
		t.Fatalf("sources=%+v", snapshot.Sources)
	}
	if !strings.Contains(snapshot.Text, "shared") {
		t.Fatalf("text=%q", snapshot.Text)
	}
}

func TestLoadRejectsInvalidUTF8(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.md")
	if err := os.WriteFile(path, []byte{'a', 0xff, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load([]string{path}); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}

func TestManagerReloadsOnAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	writeFile(t, path, "first")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager, err := New(ctx, []string{path}, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager.Start()
	defer func() {
		manager.Close()
		select {
		case <-manager.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("manager did not stop")
		}
	}()
	if got := manager.Snapshot(); got.Version != 1 || !strings.Contains(got.Text, "first") {
		t.Fatalf("initial snapshot=%+v", got)
	}

	// 原子替换（temp+rename）是最小一致写入形态。
	temp := filepath.Join(dir, "AGENTS.md.tmp")
	writeFile(t, temp, "second")
	if err := os.Rename(temp, path); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, manager, "second")
	if got := manager.Snapshot(); got.Version != 2 {
		t.Fatalf("version=%d want 2", got.Version)
	}
	// 内容不变不提升版本。
	previous := manager.Snapshot().Version
	temp = filepath.Join(dir, "AGENTS.md.tmp")
	writeFile(t, temp, "second")
	if err := os.Rename(temp, path); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := manager.Snapshot().Version; got != previous {
		t.Fatalf("identical content bumped version: %d -> %d", previous, got)
	}
}

func TestManagerKeepsVersionWhenFileDisappearsAndRecovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	writeFile(t, path, "keep me")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager, err := New(ctx, []string{path}, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager.Start()
	defer func() {
		manager.Close()
		<-manager.Done()
	}()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for manager.Status().LastError == "" {
		if time.Now().After(deadline) {
			t.Fatal("disappearance was not recorded as a transient error")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := manager.Snapshot(); !strings.Contains(got.Text, "keep me") {
		t.Fatalf("旧版本被清空: %+v", got)
	}
	writeFile(t, path, "back again")
	waitSnapshot(t, manager, "back again")
}

func TestManagerReconfigureSwapsOnlyWhenValid(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.md")
	second := filepath.Join(dir, "second.md")
	writeFile(t, first, "one")
	writeFile(t, second, "two")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager, err := New(ctx, []string{first}, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager.Start()
	defer func() {
		manager.Close()
		<-manager.Done()
	}()

	if err := manager.Reconfigure([]string{second}); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, manager, "two")
	if got := manager.Snapshot(); strings.Contains(got.Text, "one") {
		t.Fatalf("removed path still present: %+v", got)
	}
	// 非法配置保留旧版本并记录错误。
	invalid := filepath.Join(dir, "invalid.md")
	if err := os.WriteFile(invalid, []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconfigure([]string{invalid}); err == nil {
		t.Fatal("invalid configuration accepted")
	}
	if got := manager.Snapshot(); !strings.Contains(got.Text, "two") {
		t.Fatalf("failed reconfigure replaced the snapshot: %+v", got)
	}
	if manager.Status().LastError == "" || manager.Status().Generation != 2 {
		t.Fatalf("status=%+v", manager.Status())
	}
}

func waitSnapshot(t *testing.T, manager *Manager, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if strings.Contains(manager.Snapshot().Text, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot did not contain %q: %+v", want, manager.Snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
