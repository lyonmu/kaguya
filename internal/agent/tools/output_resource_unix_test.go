//go:build darwin || linux

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
)

var bashOutputResource = flag.Bool("bash-output-resource", false, "measure three rounds of real 16x16MiB, 64MiB command and 256MiB conversation Bash output")

type bashResourceSample struct {
	HeapBytes  uint64 `json:"heap_bytes"`
	RSSBytes   uint64 `json:"rss_bytes"`
	FDs        int    `json:"fds"`
	Goroutines int    `json:"goroutines"`
	QuotaRoots int    `json:"quota_roots"`
	Writers    int    `json:"writers"`
}

func sampleBashResources(t *testing.T) bashResourceSample {
	t.Helper()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	fdPath := "/dev/fd"
	if runtime.GOOS == "linux" {
		fdPath = "/proc/self/fd"
	}
	fds, err := os.ReadDir(fdPath)
	if err != nil {
		t.Errorf("cannot measure Bash resource descriptors: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/bin/ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Errorf("cannot measure Bash resource resident memory: %v", err)
	}
	rss, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		t.Errorf("invalid Bash resource resident memory: %v", err)
	}
	sample := bashResourceSample{HeapBytes: memory.HeapAlloc, RSSBytes: rss * 1024, FDs: len(fds), Goroutines: runtime.NumGoroutine()}
	outputQuotas.Lock()
	sample.QuotaRoots = len(outputQuotas.entries)
	for _, quota := range outputQuotas.entries {
		sample.Writers += quota.refs
	}
	outputQuotas.Unlock()
	return sample
}

func (s *bashResourceSample) include(next bashResourceSample) {
	s.HeapBytes = max(s.HeapBytes, next.HeapBytes)
	s.RSSBytes = max(s.RSSBytes, next.RSSBytes)
	s.FDs = max(s.FDs, next.FDs)
	s.Goroutines = max(s.Goroutines, next.Goroutines)
	s.QuotaRoots = max(s.QuotaRoots, next.QuotaRoots)
	s.Writers = max(s.Writers, next.Writers)
}

type bashResourceResult struct {
	Response fantasy.ToolResponse
	Err      error
}

type bashResourceJob struct {
	Set    *Set
	Done   chan bashResourceResult
	PID    int
	Path   string
	Bytes  int64
	Closed bool
}

// 每个writer先真实保存64KiB并持有quota，再报告ready；release只控制测试
// 命令生成内容，不在产品accumulator/取消/读取实现加入测量hook。
func TestBashOutputResources(t *testing.T) {
	if !*bashOutputResource {
		t.Skip("run with -bash-output-resource to collect the real output resource gate")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Fatal("Bash output resource gate requires bash", err)
	}
	oldID := global.Id
	global.Id = &testIDGenerator{next: 5234}
	t.Cleanup(func() { global.Id = oldID })
	for _, scenario := range []struct {
		Name   string
		Jobs   int
		Shared bool
		Quota  bool
	}{
		{Name: "independent_16x16MiB", Jobs: 16},
		{Name: "command_64MiB", Jobs: 1, Quota: true},
		{Name: "conversation_256MiB", Jobs: 5, Shared: true, Quota: true},
	} {
		for round := 1; round <= 3; round++ {
			t.Run(fmt.Sprintf("%s_round_%d", scenario.Name, round), func(t *testing.T) {
				before := sampleBashResources(t)
				if before.QuotaRoots != 0 || before.Writers != 0 {
					t.Fatal("previous scenario retained output owners")
				}
				started := time.Now()
				ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				defer cancel()
				tempBase, workspace := t.TempDir(), t.TempDir()
				jobs := make([]*bashResourceJob, 0, scenario.Jobs)
				var workers sync.WaitGroup
				var active atomic.Int64
				allDone := make(chan struct{})
				// Failures cancel and join the commands before closing pinned roots.
				// Cleanup errors are separate evidence, never counted as normal completion.
				t.Cleanup(func() {
					ownersBeforeCleanup := active.Load()
					cancel()
					select {
					case <-allDone:
					case <-time.After(10 * time.Second):
						t.Error("Bash resource cleanup could not join owned commands")
						return
					}
					for _, job := range jobs {
						if !job.Closed {
							if err := job.Set.Close(); err != nil {
								t.Error("Bash resource cleanup root close failed", err)
							}
							job.Closed = true
						}
					}
					if ownersBeforeCleanup != 0 {
						t.Logf("BASH_OUTPUT_RESOURCE_CLEANUP forced_cancel=true command_owners_before=%d command_owners_after=%d", ownersBeforeCleanup, active.Load())
					}
				})
				// A setup failure must not leave allDone waiting for a goroutine that
				// was never started. Add all workers before starting the joiner.
				var joinOnce sync.Once
				startJoin := func() { joinOnce.Do(func() { go func() { workers.Wait(); close(allDone) }() }) }
				defer startJoin()
				payload := []byte(strings.Repeat(strings.Repeat("x", 127)+"\n", 512))
				for index := range scenario.Jobs {
					cwd := filepath.Join(workspace, fmt.Sprintf("job-%d", index))
					if err := os.Mkdir(cwd, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(cwd, "payload.txt"), payload, 0600); err != nil {
						t.Fatal(err)
					}
					conversation := fmt.Sprintf("output-resource-%d", index)
					if scenario.Shared {
						conversation = "output-resource-shared"
					}
					set, err := newSet(cwd, conversation, tempBase, time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local), zap.NewNop())
					if err != nil {
						t.Fatal(err)
					}
					job := &bashResourceJob{Set: set, Done: make(chan bashResourceResult, 1)}
					jobs = append(jobs, job)
					command := `printf '%s\n' "$$" > leader.pid; IFS= read -r -d '' chunk < payload.txt || :; `
					if scenario.Quota {
						command += `(sleep 120; printf leaked > late-child) & `
					}
					command += `printf '%s' "$chunk"; printf ready > ready; while [ ! -e release ]; do sleep 0.01; done; `
					if scenario.Quota {
						command += `while :; do printf '%s' "$chunk"; done`
					} else {
						command += `for ((i=1;i<256;i++)); do printf '%s' "$chunk"; done; printf completed > normal-finished`
					}
					input, err := json.Marshal(BashInput{Command: command})
					if err != nil {
						t.Fatal(err)
					}
					workers.Add(1)
					active.Add(1)
					go func() {
						defer workers.Done()
						defer active.Add(-1)
						response, err := set.BashTool().Run(ctx, fantasy.ToolCall{ID: "resource", Name: "bash", Input: string(input)})
						job.Done <- bashResourceResult{Response: response, Err: err}
					}()
				}
				startJoin()
				for _, job := range jobs {
					awaitBashReady(t, ctx, job)
				}
				held := sampleBashResources(t)
				expectedRoots := scenario.Jobs
				if scenario.Shared {
					expectedRoots = 1
				}
				if held.QuotaRoots != expectedRoots || held.Writers != scenario.Jobs || active.Load() != int64(scenario.Jobs) {
					t.Fatalf("real writers did not concurrently hold their quotas: %+v", held)
				}
				observed := held
				observed.include(before)
				var sampleMu sync.Mutex
				stopSample, sampleDone := make(chan struct{}), make(chan struct{})
				go func() {
					defer close(sampleDone)
					fast, slow := time.NewTicker(10*time.Millisecond), time.NewTicker(100*time.Millisecond)
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
							next := sampleBashResources(t)
							sampleMu.Lock()
							observed.include(next)
							sampleMu.Unlock()
						}
					}
				}()
				var finishSampleOnce sync.Once
				finishSample := func() { finishSampleOnce.Do(func() { close(stopSample); <-sampleDone }) }
				defer finishSample()
				for _, job := range jobs {
					if err := os.WriteFile(filepath.Join(job.Set.cwd, "release"), []byte("release"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				var totalBytes int64
				for _, job := range jobs {
					var result bashResourceResult
					select {
					case result = <-job.Done:
					case <-ctx.Done():
						t.Fatal("real Bash output did not complete before the resource deadline", ctx.Err())
					}
					if result.Err != nil || result.Response.IsError != scenario.Quota || strings.Contains(result.Response.Content, "Output limit reached") != scenario.Quota {
						t.Fatal("unexpected real command result", result.Err, result.Response.IsError)
					}
					var metadata struct {
						Path string `json:"fullOutputPath"`
					}
					if err := json.Unmarshal([]byte(result.Response.Metadata), &metadata); err != nil || metadata.Path == "" {
						t.Fatal("missing captured output path", err)
					}
					job.Path = metadata.Path
					job.Bytes = verifyBashPayload(t, job.Path, payload)
					if job.Bytes > maxCommandOutputBytes || !scenario.Quota && job.Bytes != 16<<20 {
						t.Fatal("real output has an unexpected file size", job.Bytes)
					}
					if scenario.Jobs == 1 && job.Bytes != maxCommandOutputBytes {
						t.Fatal("command boundary did not retain exactly 64MiB")
					}
					totalBytes += job.Bytes
					verifyBashResourcePages(t, ctx, job)
					awaitBashGroupExit(t, job.PID)
					if _, err := os.Stat(filepath.Join(job.Set.cwd, "late-child")); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("quota child side effect survived", err)
					}
					if !scenario.Quota {
						if _, err := os.Stat(filepath.Join(job.Set.cwd, "normal-finished")); err != nil {
							t.Fatal("finite command failed to finish normally", err)
						}
					}
				}
				<-allDone
				if active.Load() != 0 {
					t.Fatal("output command owners did not exit")
				}
				if scenario.Shared && totalBytes != maxConversationOutputBytes {
					t.Fatal("shared directory did not retain exactly 256MiB", totalBytes)
				}
				if !scenario.Quota && totalBytes != 256<<20 {
					t.Fatal("independent commands did not really save 16x16MiB", totalBytes)
				}
				for index, job := range jobs {
					if actual := conversationOutputSize(t, filepath.Join(job.Set.tempBase, job.Set.conversationOutputDir())); actual > maxConversationOutputBytes || scenario.Shared && actual != totalBytes || !scenario.Shared && actual != job.Bytes {
						t.Fatal("on-disk directory accounting differs from captured bytes", actual)
					}
					if !scenario.Shared && len(jobs) > 1 {
						other := jobs[(index+1)%len(jobs)]
						response, err := other.Set.ReadTool().Run(ctx, fantasy.ToolCall{ID: "cross-conversation", Name: "read", Input: mustBashReadInput(t, job.Path, 1, 1)})
						if err != nil || !response.IsError {
							t.Fatal("other conversation accepted captured output", err)
						}
					}
				}
				for _, job := range jobs {
					if err := job.Set.Close(); err != nil {
						t.Fatal("owned Set roots did not close", err)
					}
					job.Closed = true
				}
				finishSample()
				after := sampleBashResources(t)
				if after.QuotaRoots != 0 || after.Writers != 0 || after.FDs != before.FDs {
					t.Fatalf("Bash resources did not converge: before=%+v after=%+v", before, after)
				}
				sampleMu.Lock()
				observed.include(after)
				peak := observed
				sampleMu.Unlock()
				fileBytes := make([]int64, 0, len(jobs))
				for _, job := range jobs {
					fileBytes = append(fileBytes, job.Bytes)
				}
				data, err := json.Marshal(map[string]any{"scenario": scenario.Name, "round": round, "elapsed_ms": float64(time.Since(started)) / float64(time.Millisecond), "saved_bytes": totalBytes, "file_bytes": fileBytes, "commands": len(jobs), "before": before, "held": held, "observed_peak": peak, "after": after, "command_owners_after": active.Load(), "process_groups_after": 0, "sets_after": 0, "pages_verified": 3 * len(jobs)})
				if err != nil {
					t.Fatal(err)
				}
				t.Log("BASH_OUTPUT_RESOURCE", string(data))
			})
		}
	}
}

func awaitBashReady(t *testing.T, ctx context.Context, job *bashResourceJob) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(job.Set.cwd, "ready")); err == nil {
			data, err := os.ReadFile(filepath.Join(job.Set.cwd, "leader.pid"))
			if err != nil {
				t.Fatal(err)
			}
			job.PID, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || job.PID <= 0 || syscall.Kill(-job.PID, 0) != nil {
				t.Fatal("ready Bash has no live process group", err)
			}
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case result := <-job.Done:
			t.Fatal("Bash exited before holding its real output quota", result.Err, result.Response.IsError)
		case <-ctx.Done():
			t.Fatal("Bash ready deadline expired", ctx.Err())
		case <-ticker.C:
		}
	}
}

func awaitBashGroupExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(-pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatal("real output process group did not exit", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Streaming verification proves actual bytes; no sparse fixture, no full-file buffer.
func verifyBashPayload(t *testing.T, path string, pattern []byte) int64 {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error("captured resource reader did not close", err)
		}
	}()
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("captured resource file has incorrect permissions", err)
	}
	buffer := make([]byte, len(pattern))
	var total int64
	for {
		n, err := io.ReadFull(file, buffer)
		if !bytes.Equal(buffer[:n], pattern[:n]) {
			t.Fatal("real captured bytes do not match their generated prefix")
		}
		total += int64(n)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return total
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func mustBashReadInput(t *testing.T, path string, offset, limit int) string {
	t.Helper()
	input, err := json.Marshal(ReadInput{Path: path, Offset: &offset, Limit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	return string(input)
}

func verifyBashResourcePages(t *testing.T, ctx context.Context, job *bashResourceJob) {
	t.Helper()
	lines := int((job.Bytes + 127) / 128)
	if lines < 3 {
		t.Fatal("real resource output has no full pages")
	}
	for _, offset := range []int{1, max(1, lines/2), max(1, lines-2)} {
		response, err := job.Set.ReadTool().Run(ctx, fantasy.ToolCall{ID: "resource-read", Name: "read", Input: mustBashReadInput(t, job.Path, offset, 3)})
		if err != nil || response.IsError || !strings.HasPrefix(response.Content, strings.Repeat("x", 127)+"\n") {
			t.Fatal("current conversation could not read a real output page", err)
		}
		if offset == 1 && !strings.Contains(response.Content, "offset=4") {
			t.Fatal("first output page has no correct continuation")
		}
		var metadata struct {
			Truncation Truncation `json:"truncation"`
		}
		if err := json.Unmarshal([]byte(response.Metadata), &metadata); err != nil || metadata.Truncation.TotalLines != lines || metadata.Truncation.OutputLines != 3 {
			t.Fatal("real output pagination metadata is incorrect", err)
		}
	}
}
