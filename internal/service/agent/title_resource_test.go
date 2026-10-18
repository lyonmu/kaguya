package agent

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyatitlejob"
)

var titleResource = flag.Bool("title-resource", false, "measure three rounds of 1000 durable SQLCipher title jobs")

// 只包装测试driver，不在产品执行链加入测量hook。单连接保证事务互斥；
// 从请求Tx之前计时，至实际成功COMMIT完成，包含池等待、查询和两次写入。
type titleTimingDriver struct {
	dialect.Driver
	mu      sync.Mutex
	active  *titleTimingTx
	flushes []time.Duration
}

func (d *titleTimingDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	started := time.Now()
	tx, err := d.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	wrapped := &titleTimingTx{Tx: tx, owner: d, started: started}
	d.mu.Lock()
	d.active = wrapped
	d.mu.Unlock()
	return wrapped, nil
}

type titleTimingTx struct {
	dialect.Tx
	owner   *titleTimingDriver
	started time.Time
	success bool // only accessed under owner.mu
}

func (tx *titleTimingTx) Commit() error {
	err := tx.Tx.Commit()
	tx.complete(err == nil)
	return err
}

func (tx *titleTimingTx) Rollback() error {
	err := tx.Tx.Rollback()
	tx.complete(false)
	return err
}

func (tx *titleTimingTx) complete(committed bool) {
	tx.owner.mu.Lock()
	defer tx.owner.mu.Unlock()
	if committed && tx.success {
		tx.owner.flushes = append(tx.owner.flushes, time.Since(tx.started))
		tx.success = false
	}
	if tx.owner.active == tx {
		tx.owner.active = nil
	}
}

type titleResourceSample struct {
	HeapBytes  uint64 `json:"heap_bytes"`
	RSSBytes   uint64 `json:"rss_bytes"`
	FDs        int    `json:"fds"`
	Goroutines int    `json:"goroutines"`
}

func sampleTitleResources(t *testing.T) titleResourceSample {
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
	return titleResourceSample{HeapBytes: memory.HeapAlloc, RSSBytes: rss * 1024, FDs: len(fds), Goroutines: runtime.NumGoroutine()}
}

func (s *titleResourceSample) include(next titleResourceSample) {
	s.HeapBytes = max(s.HeapBytes, next.HeapBytes)
	s.RSSBytes = max(s.RSSBytes, next.RSSBytes)
	s.FDs = max(s.FDs, next.FDs)
	s.Goroutines = max(s.Goroutines, next.Goroutines)
}

type titleLatencySample struct {
	Count int     `json:"count"`
	P50MS float64 `json:"p50_ms"`
	P95MS float64 `json:"p95_ms"`
	MaxMS float64 `json:"max_ms"`
}

func titleLatency(durations []time.Duration) titleLatencySample {
	ordered := slices.Clone(durations)
	slices.Sort(ordered)
	if len(ordered) == 0 {
		return titleLatencySample{}
	}
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	// nearest-rank quantiles, fixed for baseline and current measurements.
	return titleLatencySample{Count: len(ordered), P50MS: ms(ordered[(len(ordered)*50+99)/100-1]), P95MS: ms(ordered[(len(ordered)*95+99)/100-1]), MaxMS: ms(ordered[len(ordered)-1])}
}

// 资源负载显式启用；通常make test仍执行功能/故障回归，不静默宣称负载已运行。
func TestTitleQueueResources(t *testing.T) {
	if !*titleResource {
		t.Skip("run with -title-resource to collect the release resource gate")
	}
	for round := 1; round <= 3; round++ {
		before := sampleTitleResources(t)
		t.Run(fmt.Sprintf("round_%d", round), func(t *testing.T) {
			ctx, _, pool, _, queue, events, _ := setupTitleFault(t)
			probe := &titleTimingDriver{Driver: entsql.OpenDB(dialect.SQLite, pool)}
			client := ent.NewClient(ent.Driver(probe))
			queue.client = client
			// 原client拥有同一个实际pool，由fixture在worker退出后关闭。
			client.KaguyaConversation.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					m := mutation.(*ent.KaguyaConversationMutation)
					if _, present := m.Title(); present && m.Op().Is(ent.OpUpdate) {
						probe.mu.Lock()
						if probe.active != nil {
							probe.active.success = true
						}
						probe.mu.Unlock()
					}
					return next.Mutate(ctx, mutation)
				})
			})
			var active, peak, calls atomic.Int32
			var orderMu sync.Mutex
			order := make([]string, 0, 1000)
			configureTitleModel(t, ctx, client, func(w http.ResponseWriter, r *http.Request) {
				current := active.Add(1)
				defer active.Add(-1)
				for previous := peak.Load(); current > previous && !peak.CompareAndSwap(previous, current); previous = peak.Load() {
				}
				calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				orderMu.Lock()
				order = append(order, titlePrompt(body))
				orderMu.Unlock()
				okTitleResponse(w, "资源队列标题")
			})
			// 不把慢消费者overflow的副作用混入队列测量。自有reader退出后join。
			readStop, readDone := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(readDone)
				for {
					select {
					case <-readStop:
						return
					case _, open := <-events:
						if !open {
							return
						}
					}
				}
			}()
			defer func() { close(readStop); <-readDone }()
			startSample := sampleTitleResources(t)
			var samplesMu sync.Mutex
			resourcePeak := startSample
			sampleStop, sampleDone := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(sampleDone)
				clock := time.NewTicker(100 * time.Millisecond)
				defer clock.Stop()
				for {
					select {
					case <-sampleStop:
						return
					case <-clock.C:
						sample := sampleTitleResources(t)
						samplesMu.Lock()
						resourcePeak.include(sample)
						samplesMu.Unlock()
					}
				}
			}()
			defer func() { close(sampleStop); <-sampleDone }()
			statsBefore := pool.Stats()
			started := time.Now()
			enqueue := make([]time.Duration, 0, 1000)
			for i := 0; i < 1000; i++ {
				id := fmt.Sprintf("resource-%04d", i)
				createTitleConversation(t, ctx, client, id)
				start := time.Now()
				if err := queue.Enqueue(ctx, id, id, ""); err != nil {
					t.Fatal(err)
				}
				enqueue = append(enqueue, time.Since(start))
			}
			queue.Start()
			for i := 0; i < 1000; i++ {
				if err := queue.Wait(ctx, fmt.Sprintf("resource-%04d", i)); err != nil {
					t.Fatal(err)
				}
			}
			queue.Close()
			waitTitleQueueClosed(t, queue)
			elapsed := time.Since(started)
			queue.mu.Lock()
			waiters, attempts, cancels := len(queue.waiters), len(queue.attempts), len(queue.cancelRequested)
			queue.mu.Unlock()
			if waiters != 0 || attempts != 0 || cancels != 0 || active.Load() != 0 || peak.Load() != 1 || calls.Load() != 1000 {
				t.Fatalf("queue did not converge: waiters=%d attempts=%d cancels=%d active=%d peak=%d calls=%d", waiters, attempts, cancels, active.Load(), peak.Load(), calls.Load())
			}
			jobs, err := client.KaguyaTitleJob.Query().All(ctx)
			if err != nil || len(jobs) != 1000 {
				t.Fatalf("durable jobs=%d err=%v", len(jobs), err)
			}
			for _, job := range jobs {
				if job.Status != kaguyatitlejob.StatusSucceeded || job.AttemptCount != 1 {
					t.Fatalf("unfinished/duplicated durable job: status=%s attempts=%d", job.Status, job.AttemptCount)
				}
			}
			if count, err := client.KaguyaTaskUsage.Query().Count(ctx); err != nil || count != 1000 {
				t.Fatalf("durable usage rows=%d err=%v", count, err)
			}
			orderMu.Lock()
			if len(order) != 1000 {
				t.Fatalf("FIFO calls=%d", len(order))
			}
			for i, prompt := range order {
				if !strings.Contains(prompt, fmt.Sprintf("resource-%04d", i)) {
					t.Fatalf("FIFO violated at %d", i)
				}
			}
			orderMu.Unlock()
			probe.mu.Lock()
			flushes := slices.Clone(probe.flushes)
			probe.mu.Unlock()
			if len(flushes) != 1000 {
				t.Fatalf("success transaction measurement count=%d", len(flushes))
			}
			statsAfter := pool.Stats()
			if statsAfter.InUse != 0 {
				t.Fatalf("pool retained active connection: %d", statsAfter.InUse)
			}
			samplesMu.Lock()
			peakSample := resourcePeak
			samplesMu.Unlock()
			report := struct {
				Round     int                 `json:"round"`
				ElapsedMS float64             `json:"elapsed_ms"`
				Enqueue   titleLatencySample  `json:"enqueue"`
				Flush     titleLatencySample  `json:"success_tx"`
				Start     titleResourceSample `json:"start"`
				Peak      titleResourceSample `json:"peak_sampled_100ms"`
				After     titleResourceSample `json:"after_done"`
				DBWaits   int64               `json:"db_waits"`
				DBWaitMS  float64             `json:"db_wait_ms"`
				PoolInUse int                 `json:"pool_in_use"`
			}{round, float64(elapsed) / float64(time.Millisecond), titleLatency(enqueue), titleLatency(flushes), startSample, peakSample, sampleTitleResources(t), statsAfter.WaitCount - statsBefore.WaitCount, float64(statsAfter.WaitDuration-statsBefore.WaitDuration) / float64(time.Millisecond), statsAfter.InUse}
			raw, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("TITLE_RESOURCE %s", raw)
		})
		// 子测试cleanup已关闭mock HTTP、publisher及数据库；总goroutine仅记录。
		after := sampleTitleResources(t)
		if after.FDs > before.FDs {
			t.Fatalf("fixture retained descriptors: before=%d after=%d", before.FDs, after.FDs)
		}
		raw, err := json.Marshal(struct {
			Round  int                 `json:"round"`
			Before titleResourceSample `json:"before_fixture"`
			After  titleResourceSample `json:"after_cleanup"`
		}{round, before, after})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("TITLE_RESOURCE_CLEANUP %s", raw)
	}
}
