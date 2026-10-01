package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyatitlejob"
	"go.uber.org/zap"
)

func configureTitleModel(t *testing.T, ctx context.Context, client *ent.Client, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("queue").SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("task").SetModelID("queue-model").SetRequestPath("/v1/chat/completions").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetTaskModelID(model.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
}

func createTitleConversation(t *testing.T, ctx context.Context, client *ent.Client, id string) {
	t.Helper()
	if _, err := client.KaguyaConversation.Create().SetID(id).SetTitle(defaultConversationTitle).
		SetModelID("model").SetModelName("Model").SetLastMessageAt(time.Now()).Save(ctx); err != nil {
		t.Fatal(err)
	}
}

func titlePrompt(body []byte) string {
	var parsed struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Messages) == 0 {
		return ""
	}
	return parsed.Messages[len(parsed.Messages)-1].Content
}

func okTitleResponse(w http.ResponseWriter, title string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":"t","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, title)
}

// 标题任务持久入队并按 enqueue_order FIFO 串行执行，不因 worker 忙而跳过。
func TestTitleQueueFIFO(t *testing.T) {
	ctx, client := setupChatTest(t)
	var mu sync.Mutex
	var order []string
	configureTitleModel(t, ctx, client, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		question := titlePrompt(raw)
		mu.Lock()
		order = append(order, question)
		mu.Unlock()
		title := "标题A"
		if strings.Contains(question, "qb") {
			title = "标题B"
		}
		okTitleResponse(w, title)
	})
	createTitleConversation(t, ctx, client, "conv-a")
	createTitleConversation(t, ctx, client, "conv-b")
	if err := DefaultTitleQueue.Enqueue(ctx, "conv-a", "qa", ""); err != nil {
		t.Fatal(err)
	}
	if err := DefaultTitleQueue.Enqueue(ctx, "conv-b", "qb", ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"conv-a", "conv-b"} {
		if err := DefaultTitleQueue.Wait(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	got := append([]string{}, order...)
	mu.Unlock()
	if len(got) != 2 || !strings.Contains(got[0], "qa") || !strings.Contains(got[1], "qb") {
		t.Fatalf("FIFO order=%v", got)
	}
	for id, want := range map[string]string{"conv-a": "标题A", "conv-b": "标题B"} {
		row, err := client.KaguyaConversation.Get(ctx, id)
		if err != nil || row.Title != want {
			t.Fatalf("%s title=%q err=%v", id, row.Title, err)
		}
	}
	if count, err := client.KaguyaTitleJob.Query().Count(ctx); err != nil || count != 2 {
		t.Fatalf("job count=%d err=%v", count, err)
	}
}

// pending 期间重复入队幂等；标题仍是默认值时才允许终态重试。
func TestTitleQueueEnqueueIsIdempotent(t *testing.T) {
	ctx, client := setupChatTest(t)
	release := make(chan struct{})
	released := false
	started := make(chan struct{}, 1)
	configureTitleModel(t, ctx, client, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		okTitleResponse(w, "幂等标题")
	})
	defer func() {
		if !released {
			close(release)
		}
	}()
	createTitleConversation(t, ctx, client, "conv-a")
	if err := DefaultTitleQueue.Enqueue(ctx, "conv-a", "q1", ""); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := DefaultTitleQueue.Enqueue(ctx, "conv-a", "q2", ""); err != nil {
		t.Fatal(err)
	}
	if count, err := client.KaguyaTitleJob.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("job count=%d err=%v", count, err)
	}
	close(release)
	released = true
	if err := DefaultTitleQueue.Wait(ctx, "conv-a"); err != nil {
		t.Fatal(err)
	}
	// 标题已非默认：再次显式入队不得启动新尝试。
	if err := DefaultTitleQueue.Enqueue(ctx, "conv-a", "q3", ""); err != nil {
		t.Fatal(err)
	}
	job, err := client.KaguyaTitleJob.Query().Where(kaguyatitlejob.ConversationIDEQ("conv-a")).Only(ctx)
	if err != nil || job.Status != kaguyatitlejob.StatusSucceeded {
		t.Fatalf("job=%+v err=%v", job, err)
	}
}

// 单个任务失败只终结自身，不阻塞后续任务。
func TestTitleQueueFailureDoesNotBlockNext(t *testing.T) {
	ctx, client := setupChatTest(t)
	configureTitleModel(t, ctx, client, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(titlePrompt(raw), "fail") {
			http.Error(w, "provider down", http.StatusInternalServerError)
			return
		}
		okTitleResponse(w, "成功标题")
	})
	createTitleConversation(t, ctx, client, "conv-fail")
	createTitleConversation(t, ctx, client, "conv-ok")
	if err := DefaultTitleQueue.Enqueue(ctx, "conv-fail", "fail", ""); err != nil {
		t.Fatal(err)
	}
	if err := DefaultTitleQueue.Enqueue(ctx, "conv-ok", "ok", ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"conv-fail", "conv-ok"} {
		if err := DefaultTitleQueue.Wait(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	failed, err := client.KaguyaTitleJob.Query().Where(kaguyatitlejob.ConversationIDEQ("conv-fail")).Only(ctx)
	if err != nil || failed.Status != kaguyatitlejob.StatusFailed || failed.ErrorCode == "" {
		t.Fatalf("failed job=%+v err=%v", failed, err)
	}
	ok, err := client.KaguyaTitleJob.Query().Where(kaguyatitlejob.ConversationIDEQ("conv-ok")).Only(ctx)
	if err != nil || ok.Status != kaguyatitlejob.StatusSucceeded {
		t.Fatalf("ok job=%+v err=%v", ok, err)
	}
	row, err := client.KaguyaConversation.Get(ctx, "conv-ok")
	if err != nil || row.Title != "成功标题" {
		t.Fatalf("title=%q err=%v", row.Title, err)
	}
}

// 启动恢复把遗留 running 还原为 pending，pending 保留到下一次启动。
func TestReconcileTitleJobsRestoresRunning(t *testing.T) {
	ctx, client := setupChatTest(t)
	createTitleConversation(t, ctx, client, "conv-a")
	if _, err := client.KaguyaTitleJob.Create().SetConversationID("conv-a").
		SetEnqueueOrder(1).SetQuestion("q").SetStatus(kaguyatitlejob.StatusRunning).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileTitleJobs(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := client.KaguyaTitleJob.Query().Where(kaguyatitlejob.ConversationIDEQ("conv-a")).Only(ctx)
	if err != nil || job.Status != kaguyatitlejob.StatusPending {
		t.Fatalf("job=%+v err=%v", job, err)
	}
}

// 关停在执行预算内取消模型调用，并把 running 还原为 pending 供下一次启动继续。
func TestTitleQueueShutdownRevertsRunningToPending(t *testing.T) {
	ctx, client := setupChatTest(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	configureTitleModel(t, ctx, client, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	createTitleConversation(t, ctx, client, "conv-a")
	queue := NewTitleQueue(context.Background(), client, zap.NewNop())
	queue.Start()
	if err := queue.Enqueue(ctx, "conv-a", "q", ""); err != nil {
		t.Fatal(err)
	}
	<-started
	queue.Close()
	select {
	case <-queue.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("queue worker did not exit")
	}
	job, err := client.KaguyaTitleJob.Query().Where(kaguyatitlejob.ConversationIDEQ("conv-a")).Only(ctx)
	if err != nil || job.Status != kaguyatitlejob.StatusPending {
		t.Fatalf("job=%+v err=%v", job, err)
	}
}
