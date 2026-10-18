//go:build darwin || linux

package instructions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var instructionResource = flag.Bool("instruction-resource", false, "measure three rounds of 1MiB/16MiB instruction reloads with concurrent snapshots and configuration changes")

type instructionResourceSample struct {
	HeapBytes  uint64 `json:"heap_bytes"`
	RSSBytes   uint64 `json:"rss_bytes"`
	FDs        int    `json:"fds"`
	Goroutines int    `json:"goroutines"`
}

func sampleInstructionResources(t *testing.T) instructionResourceSample {
	t.Helper()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	fdPath := "/dev/fd"
	if runtime.GOOS == "linux" {
		fdPath = "/proc/self/fd"
	}
	fds, err := os.ReadDir(fdPath)
	if err != nil {
		t.Errorf("cannot measure open descriptors: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/bin/ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Errorf("cannot measure resident memory: %v", err)
	}
	rss, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		t.Errorf("invalid resident memory measurement: %v", err)
	}
	return instructionResourceSample{HeapBytes: memory.HeapAlloc, RSSBytes: rss * 1024, FDs: len(fds), Goroutines: runtime.NumGoroutine()}
}

func (s *instructionResourceSample) include(next instructionResourceSample) {
	s.HeapBytes = max(s.HeapBytes, next.HeapBytes)
	s.RSSBytes = max(s.RSSBytes, next.RSSBytes)
	s.FDs = max(s.FDs, next.FDs)
	s.Goroutines = max(s.Goroutines, next.Goroutines)
}

// 仅在真实Load返回第二份大文件后暂停发布，测试配置代际/Close的交错。
// 字节读取、双读一致性、watcher与快照发布仍全部执行生产实现。
func TestInstructionReloadResources(t *testing.T) {
	if !*instructionResource {
		t.Skip("run with -instruction-resource to collect the release resource gate")
	}
	for _, size := range []int{1 << 20, 16 << 20} {
		for round := 1; round <= 3; round++ {
			t.Run(fmt.Sprintf("bytes_%d_round_%d", size, round), func(t *testing.T) {
				dir := t.TempDir()
				initial, stale, latest := filepath.Join(dir, "initial.md"), filepath.Join(dir, "stale.md"), filepath.Join(dir, "latest.md")
				for index, path := range []string{initial, stale, latest} {
					if err := os.WriteFile(path, []byte(strings.Repeat(string(rune('a'+index)), size)), 0600); err != nil {
						t.Fatal(err)
					}
				}
				runtime.GC()
				before := sampleInstructionResources(t)
				observed := before
				var sampleMu sync.Mutex
				stopSample, sampleDone := make(chan struct{}), make(chan struct{})
				go func() {
					defer close(sampleDone)
					fast, slow := time.NewTicker(time.Millisecond), time.NewTicker(100*time.Millisecond)
					defer fast.Stop()
					defer slow.Stop()
					for {
						select {
						case <-stopSample:
							return
						case <-fast.C:
							var memory runtime.MemStats
							runtime.ReadMemStats(&memory)
							sampleMu.Lock()
							observed.HeapBytes = max(observed.HeapBytes, memory.HeapAlloc)
							sampleMu.Unlock()
						case <-slow.C:
							sample := sampleInstructionResources(t)
							sampleMu.Lock()
							observed.include(sample)
							sampleMu.Unlock()
						}
					}
				}()
				var stopSampleOnce sync.Once
				finishSample := func() { stopSampleOnce.Do(func() { close(stopSample); <-sampleDone }) }
				defer finishSample()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				started := time.Now()
				manager, err := New(ctx, []string{initial}, Options{}) // 生产去抖/settle预算。
				if err != nil {
					t.Fatal(err)
				}
				initialSnapshot := manager.Snapshot()
				initialHash := initialSnapshot.Hash
				initialSnapshot = nil
				staleRead, releaseStale := make(chan struct{}), make(chan struct{})
				closeRead, releaseClose := make(chan struct{}), make(chan struct{})
				var staleCalls, inflight, peak, loaderCalls atomic.Int64
				var closeGate atomic.Bool
				var staleOnce, closeOnce sync.Once
				manager.load = func(ctx context.Context, paths []string) (*Snapshot, map[string]struct{}, error) {
					current := inflight.Add(1)
					defer inflight.Add(-1)
					for previous := peak.Load(); current > previous && !peak.CompareAndSwap(previous, current); previous = peak.Load() {
					}
					loaderCalls.Add(1)
					snapshot, watches, err := Load(ctx, paths)
					if err != nil {
						return snapshot, watches, err
					}
					if len(paths) == 1 && paths[0] == stale && staleCalls.Add(1) == 2 {
						close(staleRead)
						<-releaseStale
					}
					if closeGate.CompareAndSwap(true, false) {
						close(closeRead)
						<-ctx.Done()
						<-releaseClose
					}
					return snapshot, watches, err
				}
				stopSnapshots := make(chan struct{})
				var snapshotWorkers sync.WaitGroup
				var snapshotCalls atomic.Int64
				invalidSnapshot := make(chan string, 1)
				for range 8 {
					snapshotWorkers.Add(1)
					go func() {
						defer snapshotWorkers.Done()
						lastHash := ""
						for {
							select {
							case <-stopSnapshots:
								return
							default:
							}
							snapshot := manager.Snapshot()
							snapshotCalls.Add(1)
							if snapshot.Hash != lastHash {
								hash := sha256.Sum256([]byte(snapshot.Text))
								valid := snapshot.Hash == hex.EncodeToString(hash[:16]) && snapshot.Version > 0 && len(snapshot.Sources) == 1 && len(snapshot.Sources[0].Text) == size && snapshot.Sources[0].Path != stale
								if !valid {
									select {
									case invalidSnapshot <- "mixed text/hash or stale generation published":
									default:
									}
								}
								lastHash = snapshot.Hash
							}
							// Snapshot压力持续贯穿读/发布，且不把忙循环当生产工作量。
							runtime.Gosched()
						}
					}()
				}
				var stopSnapshotsOnce sync.Once
				finishSnapshots := func() { stopSnapshotsOnce.Do(func() { close(stopSnapshots); snapshotWorkers.Wait() }) }
				t.Cleanup(func() {
					staleOnce.Do(func() { close(releaseStale) })
					closeOnce.Do(func() { close(releaseClose) })
					if manager != nil {
						manager.Close()
						<-manager.Done()
					}
					finishSnapshots()
				})
				manager.Start()
				manager.Start()
				if err := manager.Reconfigure([]string{stale}); err != nil {
					t.Fatal(err)
				}
				awaitInstructionResource(t, ctx, staleRead)
				if err := manager.Reconfigure([]string{latest}); err != nil {
					t.Fatal(err)
				}
				if manager.Snapshot().Hash != initialHash || manager.Status().LoadedGeneration != 0 {
					t.Fatal("a configuration published before the reader completed")
				}
				staleOnce.Do(func() { close(releaseStale) })
				awaitInstructionGeneration(t, ctx, manager, 2)
				latestSnapshot := manager.Snapshot()
				if latestSnapshot.Version != 2 || latestSnapshot.Hash == initialHash || latestSnapshot.Sources[0].Path != latest {
					t.Fatal("latest generation did not replace the stale read")
				}
				latestHash, outputBytes := latestSnapshot.Hash, len(latestSnapshot.Text)
				latestSnapshot = nil
				if err := manager.Reconfigure([]string{latest}); err != nil {
					t.Fatal(err)
				}
				awaitInstructionGeneration(t, ctx, manager, 3)
				if snapshot := manager.Snapshot(); snapshot.Hash != latestHash || snapshot.Version != 2 {
					t.Fatal("the same hash advanced the version")
				}
				closeGate.Store(true)
				if err := manager.Reconfigure([]string{initial}); err != nil {
					t.Fatal(err)
				}
				awaitInstructionResource(t, ctx, closeRead)
				manager.Close()
				select {
				case <-manager.Done():
					t.Fatal("Done completed while a loader still held a large candidate")
				default:
				}
				closeOnce.Do(func() { close(releaseClose) })
				awaitInstructionResource(t, ctx, manager.Done())
				finishSnapshots()
				if inflight.Load() != 0 || peak.Load() != 1 || manager.Snapshot().Hash != latestHash || manager.Status().LoadedGeneration != 3 {
					t.Fatal("loader ownership or canceled publication did not converge")
				}
				if err := manager.Reconfigure(nil); !errors.Is(err, ErrClosed) {
					t.Fatalf("closed manager accepted new work: %v", err)
				}
				select {
				case problem := <-invalidSnapshot:
					t.Fatal(problem)
				default:
				}
				elapsed := time.Since(started)
				endpoint := sampleInstructionResources(t)
				finishSample()
				observed.include(endpoint)
				runtime.GC()
				closedRetained := sampleInstructionResources(t)
				// Closed Manager仍保留最后一份快照，所有外部引用释放后才测cleanup。
				manager = nil
				runtime.GC()
				cleanup := sampleInstructionResources(t)
				// 单独记录测试强制scavenge，不能把它当作产品自然关闭后的RSS。
				debug.FreeOSMemory()
				afterScavenge := sampleInstructionResources(t)
				if cleanup.FDs != before.FDs {
					t.Fatalf("watcher/read handles leaked: before=%d cleanup=%d", before.FDs, cleanup.FDs)
				}
				encoded, err := json.Marshal(map[string]any{
					"bytes": size, "round": round, "elapsed_ms": float64(elapsed) / float64(time.Millisecond),
					"before": before, "observed_peak": observed, "closed_manager_retained": closedRetained, "cleanup": cleanup, "test_forced_scavenge": afterScavenge,
					"snapshot_calls": snapshotCalls.Load(), "loader_calls": loaderCalls.Load(), "max_loader_inflight": peak.Load(), "active_loader_after_done": inflight.Load(), "output_bytes": outputBytes,
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("INSTRUCTION_RESOURCE %s", encoded)
			})
		}
	}
}

func awaitInstructionResource(t *testing.T, ctx context.Context, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("instruction resource operation did not finish within its test budget")
	}
}

func awaitInstructionGeneration(t *testing.T, ctx context.Context, manager *Manager, generation int64) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if manager.Status().LoadedGeneration == generation {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("generation %d did not publish: %+v", generation, manager.Status())
		}
	}
}
