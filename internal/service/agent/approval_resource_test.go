//go:build darwin || linux

package agent

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"
)

var approvalResource = flag.Bool("approval-resource", false, "record three SQLCipher rounds of six-call approval batches, expiry and registration cancellation")

// 沿用真正Fantasy并行调度及broker故障fixture，资源仪器不重新实现一套
// 审批策略、调度或DB。elapsed含fixture初始化/cleanup，不称审批单次延迟。
func TestApprovalLifecycleResources(t *testing.T) {
	if !*approvalResource {
		t.Skip("run with -approval-resource -sqlcipher to collect approval resources")
	}
	if !*sqlcipherTests {
		t.Fatal("approval resources require the actual SQLCipher fixture")
	}
	for _, workload := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"five_parallel_sixth_queued", TestParallelApprovalBatchDoesNotReplayOrLoseQueuedCall},
		{"expiry", TestApprovalBrokerTimeoutDoesNotExecute},
		{"close_during_registration", TestApprovalCloseDuringPersistenceCannotRegisterWaiter},
	} {
		for round := 1; round <= 3; round++ {
			runtime.GC()
			before := sampleChatResources(t)
			peak := before
			var mu sync.Mutex
			pendingAtProbe := 0
			oldProbe := approvalBatchResourceProbe
			approvalBatchResourceProbe = func(probeTest *testing.T, pending int) {
				sample := sampleChatResources(probeTest)
				mu.Lock()
				peak.include(sample)
				pendingAtProbe = pending
				mu.Unlock()
			}
			stop, done := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(done)
				heapTick, processTick := time.NewTicker(time.Millisecond), time.NewTicker(100*time.Millisecond)
				defer heapTick.Stop()
				defer processTick.Stop()
				for {
					select {
					case <-stop:
						return
					case <-heapTick.C:
						var m runtime.MemStats
						runtime.ReadMemStats(&m)
						fdPath := "/dev/fd"
						if runtime.GOOS == "linux" {
							fdPath = "/proc/self/fd"
						}
						fds, err := os.ReadDir(fdPath)
						if err != nil {
							t.Errorf("approval FD sample failed: %v", err)
						}
						mu.Lock()
						peak.HeapBytes = max(peak.HeapBytes, m.HeapAlloc)
						peak.FDs = max(peak.FDs, len(fds))
						peak.Goroutines = max(peak.Goroutines, runtime.NumGoroutine())
						mu.Unlock()
					case <-processTick.C:
						sample := sampleChatResources(t)
						mu.Lock()
						peak.include(sample)
						mu.Unlock()
					}
				}
			}()
			started := time.Now()
			passed := t.Run(fmt.Sprintf("%s_round_%d", workload.name, round), workload.run)
			approvalBatchResourceProbe = oldProbe
			elapsed := time.Since(started)
			close(stop)
			<-done // 不把采样子进程句柄混入结束端点。
			after := sampleChatResources(t)
			peak.include(after)
			runtime.GC()
			cleanup := sampleChatResources(t)
			approvalRegistry.Lock()
			registered := len(approvalRegistry.entries)
			approvalRegistry.Unlock()
			if !passed || cleanup.FDs != before.FDs || registered != 0 || PendingWork() != 0 {
				t.Errorf("approval resources did not settle: pass=%t fd=%d/%d registry=%d work=%d", passed, cleanup.FDs, before.FDs, registered, PendingWork())
			}
			if workload.name == "five_parallel_sixth_queued" && pendingAtProbe != 5 {
				t.Error("resource probe did not observe the actual five pending calls")
			}
			encoded, err := json.Marshal(struct {
				Workload                             string  `json:"workload"`
				Round                                int     `json:"round"`
				ElapsedMS                            float64 `json:"elapsed_ms"`
				Before, ObservedPeak, After, AfterGC chatResourceSample
				Registry                             int `json:"registry_after_cleanup"`
				PendingAtProbe                       int `json:"pending_at_probe"`
			}{workload.name, round, float64(elapsed) / float64(time.Millisecond), before, peak, after, cleanup, registered, pendingAtProbe})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("APPROVAL_RESOURCE %s", encoded)
		}
	}
}
