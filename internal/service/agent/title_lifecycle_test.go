package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func waitTitleQueueClosed(t *testing.T, queue *TitleQueue) {
	t.Helper()
	select {
	case <-queue.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("title worker did not finish before database cleanup")
	}
}

func TestTitleQueueCloseBeforeStart(t *testing.T) {
	queue := NewTitleQueue(context.Background(), nil, zap.NewNop())
	queue.Close()
	queue.Close()
	queue.Start()
	waitTitleQueueClosed(t, queue)
	if queue.started || queue.ctx.Err() == nil {
		t.Fatal("closed queue started or retained a live lifetime")
	}
}

func TestTitleQueueCanceledLifetimeRejectsEnqueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	queue := NewTitleQueue(ctx, nil, zap.NewNop())
	cancel()
	if err := queue.Enqueue(context.Background(), "stopping", "question", ""); !errors.Is(err, ErrServiceStopping) {
		t.Fatalf("canceled lifetime accepted enqueue: %v", err)
	}
	queue.Close()
	waitTitleQueueClosed(t, queue)
}

func TestTitleQueueConcurrentStartClose(t *testing.T) {
	_, client := setupChatTest(t)
	DefaultTitleQueue.Close()
	waitTitleQueueClosed(t, DefaultTitleQueue)
	for round := 0; round < 100; round++ {
		queue := NewTitleQueue(context.Background(), client, zap.NewNop())
		var workers sync.WaitGroup
		for i := 0; i < 16; i++ {
			workers.Add(1)
			go func(i int) {
				defer workers.Done()
				if i%2 == 0 {
					queue.Start()
				} else {
					queue.Close()
				}
			}(i)
		}
		workers.Wait()
		queue.Close()
		waitTitleQueueClosed(t, queue)
	}
}

func TestTitleQueueRepeatedStartKeepsOneConsumer(t *testing.T) {
	ctx, client := setupChatTest(t)
	DefaultTitleQueue.Close()
	waitTitleQueueClosed(t, DefaultTitleQueue)
	queue := NewTitleQueue(ctx, client, zap.NewNop())
	t.Cleanup(func() { queue.Close(); waitTitleQueueClosed(t, queue) })
	var active, peak, calls atomic.Int32
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	configureTitleModel(t, ctx, client, func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old && !peak.CompareAndSwap(old, current); old = peak.Load() {
		}
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		okTitleResponse(w, "唯一消费者")
	})
	for _, id := range []string{"start-a", "start-b"} {
		createTitleConversation(t, ctx, client, id)
		if err := queue.Enqueue(ctx, id, id, ""); err != nil {
			t.Fatal(err)
		}
	}
	var starters sync.WaitGroup
	for i := 0; i < 32; i++ {
		starters.Add(1)
		go func() { defer starters.Done(); queue.Start() }()
	}
	starters.Wait()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-started:
		t.Fatal("a second model call started while the first consumer was blocked")
	case <-time.After(100 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	for _, id := range []string{"start-a", "start-b"} {
		if err := queue.Wait(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if peak.Load() != 1 || calls.Load() != 2 {
		t.Fatalf("peak=%d calls=%d", peak.Load(), calls.Load())
	}
}

func TestTitleQueueThousandJobsFIFO(t *testing.T) {
	_, client := setupChatTest(t)
	DefaultTitleQueue.Close()
	waitTitleQueueClosed(t, DefaultTitleQueue)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	queue := NewTitleQueue(ctx, client, zap.NewNop())
	t.Cleanup(func() { queue.Close(); waitTitleQueueClosed(t, queue) })
	var mu sync.Mutex
	var order []string
	configureTitleModel(t, ctx, client, func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		order = append(order, titlePrompt(raw))
		mu.Unlock()
		okTitleResponse(w, "积压任务标题")
	})
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("fifo-%04d", i)
		createTitleConversation(t, ctx, client, id)
		if err := queue.Enqueue(ctx, id, id, ""); err != nil {
			t.Fatal(err)
		}
	}
	queue.Start()
	for i := 0; i < 1000; i++ {
		if err := queue.Wait(ctx, fmt.Sprintf("fifo-%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 1000 {
		t.Fatalf("completed model calls=%d, want 1000", len(order))
	}
	for i, prompt := range order {
		if !strings.Contains(prompt, fmt.Sprintf("fifo-%04d", i)) {
			t.Fatalf("FIFO index=%d prompt=%q", i, prompt)
		}
	}
}
