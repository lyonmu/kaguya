package instructions

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
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
	snapshot, _, err := Load(context.Background(), paths)
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
	snapshot, _, err := Load(context.Background(), []string{real, alias, filepath.Join(dir, "missing.md")})
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
	if _, _, err := Load(context.Background(), []string{path}); err == nil {
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
	if err := manager.Reconfigure([]string{invalid}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for manager.Status().LastError == "" {
		if time.Now().After(deadline) {
			t.Fatal("invalid asynchronous candidate was not reported")
		}
		time.Sleep(5 * time.Millisecond)
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

func TestManagerReconfigureDiscardsOldGenerationAndStartsOnce(t *testing.T) {
	dir := t.TempDir()
	initial, stale, latest := filepath.Join(dir, "initial.md"), filepath.Join(dir, "stale.md"), filepath.Join(dir, "latest.md")
	writeFile(t, initial, "initial")
	writeFile(t, stale, "stale")
	writeFile(t, latest, "latest")
	manager, err := New(context.Background(), []string{initial}, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	manager.load = func(ctx context.Context, paths []string) (*Snapshot, map[string]struct{}, error) {
		snapshot, watches, err := Load(ctx, paths)
		if len(paths) == 1 && paths[0] == stale && calls.Add(1) == 2 {
			close(entered)
			<-release
		}
		return snapshot, watches, err
	}
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); manager.Close(); <-manager.Done() })
	manager.Start()
	manager.Start()
	if err := manager.Reconfigure([]string{stale}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("second read was not reached")
	}
	returned := make(chan error, 1)
	paths := []string{latest}
	go func() { returned <- manager.Reconfigure(paths) }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("configuration waited for file reader")
	}
	paths[0] = stale // 配置必须复制调用方的 slice。
	status := manager.Status()
	if status.Generation != 2 || status.LoadedGeneration != 0 || !strings.Contains(manager.Snapshot().Text, "initial") {
		t.Fatalf("premature publication: %+v", status)
	}
	unblock()
	waitSnapshot(t, manager, "latest")
	if got := manager.Snapshot(); got.Version != 2 || strings.Contains(got.Text, "stale") {
		t.Fatalf("stale candidate was published: %+v", got)
	}
	if status := manager.Status(); status.Generation != 2 || status.LoadedGeneration != 2 {
		t.Fatalf("status=%+v", status)
	}
}

func TestManagerDoneWaitsForReaderAndCanceledReadCannotPublish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.md")
	writeFile(t, path, "initial")
	manager, err := New(context.Background(), []string{path}, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var reads atomic.Int64
	manager.load = func(ctx context.Context, paths []string) (*Snapshot, map[string]struct{}, error) {
		if reads.Add(1) != 1 {
			t.Error("multiple reader workers")
		}
		snapshot, watches, err := Load(ctx, paths)
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return snapshot, watches, err
	}
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); manager.Close(); <-manager.Done() })
	manager.Start()
	manager.Start()
	writeFile(t, path, "replacement")
	if err := manager.Reconfigure([]string{path}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("reader did not start")
	}
	manager.Close()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("reader context was not canceled")
	}
	select {
	case <-manager.Done():
		t.Fatal("Done closed while reader was still running")
	default:
	}
	unblock()
	select {
	case <-manager.Done():
	case <-time.After(time.Second):
		t.Fatal("Done did not drain reader")
	}
	if got := manager.Snapshot(); got.Version != 1 || !strings.Contains(got.Text, "initial") {
		t.Fatalf("canceled candidate was published: %+v", got)
	}
	if err := manager.Reconfigure(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed manager accepted configuration: %v", err)
	}
}

func TestManagerExplicitRemovalAndSnapshotAreIndependent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.md")
	writeFile(t, path, "initial")
	manager, err := New(context.Background(), []string{path}, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close(); <-manager.Done() })
	copy := manager.Snapshot()
	copy.Text, copy.Hash, copy.Version = "mutated", "mutated", -1
	copy.Sources[0].Path = "mutated"
	if snapshot := manager.Snapshot(); snapshot.Version != 1 || snapshot.Sources[0].Path != path || !strings.Contains(snapshot.Text, "initial") {
		t.Fatalf("caller mutated owned snapshot: %+v", snapshot)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconfigure(nil); err != nil {
		t.Fatal(err)
	}
	manager.Start()
	deadline := time.Now().Add(3 * time.Second)
	for manager.Status().LoadedGeneration != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("removed configuration was not applied: %+v", manager.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if snapshot := manager.Snapshot(); snapshot.Text != "" || len(snapshot.Sources) != 0 || snapshot.Version != 2 {
		t.Fatalf("explicit removal retained old data: %+v", snapshot)
	}
}

func TestManagerCloseBeforeStartReleasesWatcher(t *testing.T) {
	manager, err := New(context.Background(), nil, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager.Close()
	manager.Close()
	manager.Start()
	select {
	case <-manager.Done():
	case <-time.After(time.Second):
		t.Fatal("unstarted manager did not finish")
	}
	if err := manager.watcher.Add(t.TempDir()); !errors.Is(err, fsnotify.ErrClosed) {
		t.Fatalf("watcher was not closed: %v", err)
	}
}

func TestManagerDoubleReadRejectsSameContentReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.md")
	writeFile(t, path, "identical")
	manager, err := New(context.Background(), []string{path}, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	third, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	manager.load = func(ctx context.Context, paths []string) (*Snapshot, map[string]struct{}, error) {
		call := calls.Add(1)
		if call == 2 {
			temp := path + ".new"
			if err := os.WriteFile(temp, []byte("identical"), 0o600); err != nil {
				return nil, nil, err
			}
			if err := os.Rename(temp, path); err != nil {
				return nil, nil, err
			}
		}
		if call == 3 {
			close(third)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		return Load(ctx, paths)
	}
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); manager.Close(); <-manager.Done() })
	manager.Start()
	if err := manager.Reconfigure([]string{path}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-third:
	case <-time.After(3 * time.Second):
		t.Fatal("unstable source was not retried")
	}
	if status := manager.Status(); status.LoadedGeneration != 0 || status.LastError != "source_unstable" {
		t.Fatalf("identity change was accepted despite equal hash: %+v", status)
	}
	unblock()
	deadline := time.Now().Add(3 * time.Second)
	for manager.Status().LoadedGeneration != 1 {
		if time.Now().After(deadline) {
			t.Fatal("stable replacement not published")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if snapshot := manager.Snapshot(); snapshot.Version != 1 {
		t.Fatal("identical content increased version")
	}
}

func TestLoadCancellationReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Load(ctx, []string{"/does-not-exist"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled load: %v", err)
	}
	path := filepath.Join(t.TempDir(), "notes.md")
	writeFile(t, path, "content")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := readAll(ctx, file); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
}

func TestManagerWatchFailureKeepsOldSnapshotAndTracksSuccessfulAdds(t *testing.T) {
	initial := filepath.Join(t.TempDir(), "initial.md")
	one, two := filepath.Join(t.TempDir(), "one.md"), filepath.Join(t.TempDir(), "two.md")
	writeFile(t, initial, "initial")
	writeFile(t, one, "one")
	writeFile(t, two, "two")
	manager, err := New(context.Background(), []string{initial}, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	initialWatchCount := len(manager.watcher.WatchList())
	var fail atomic.Bool
	fail.Store(true)
	var attempts atomic.Int64
	manager.addWatch = func(dir string) error {
		if fail.Load() && attempts.Add(1) > 1 {
			return errors.New("injected watch failure")
		}
		return manager.watcher.Add(dir)
	}
	t.Cleanup(func() { manager.Close(); <-manager.Done() })
	manager.Start()
	if err := manager.Reconfigure([]string{one, two}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for manager.Status().LastError != "watch_failed" {
		if time.Now().After(deadline) {
			t.Fatal("watch failure was not reported")
		}
		time.Sleep(5 * time.Millisecond)
	}
	manager.mu.Lock()
	_, retained := manager.watches[filepath.Dir(initial)]
	tracked := len(manager.watches)
	manager.mu.Unlock()
	if !retained || tracked != initialWatchCount+1 || len(manager.watcher.WatchList()) != initialWatchCount+1 {
		t.Fatalf("partial add or old watch lost: retained=%v tracked=%d", retained, tracked)
	}
	if snapshot := manager.Snapshot(); !strings.Contains(snapshot.Text, "initial") || manager.Status().LoadedGeneration != 0 {
		t.Fatal("watch failure published candidate")
	}
	fail.Store(false)
	if err := manager.Reconfigure([]string{one, two}); err != nil {
		t.Fatal(err)
	}
	waitSnapshot(t, manager, "one")
	if snapshot := manager.Snapshot(); !strings.Contains(snapshot.Text, "two") || strings.Contains(snapshot.Text, "initial") {
		t.Fatalf("watch recovery did not publish entire candidate: %+v", snapshot)
	}
	manager.mu.Lock()
	_, retained = manager.watches[filepath.Dir(initial)]
	manager.mu.Unlock()
	if retained || len(manager.watcher.WatchList()) != len(WatchDirs([]string{one, two})) {
		t.Fatal("successful reconciliation retained removed configuration watch")
	}
}

func TestManagerReconcilesWatchRemovedByOS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.md")
	writeFile(t, path, "initial")
	manager, err := New(context.Background(), []string{path}, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close(); <-manager.Done() })
	manager.Start()
	if err := manager.watcher.Remove(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(manager.watcher.WatchList()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("lost watch was not restored from actual watch list")
		}
		time.Sleep(5 * time.Millisecond)
	}
	writeFile(t, path, "after repair")
	waitSnapshot(t, manager, "after repair")
}
