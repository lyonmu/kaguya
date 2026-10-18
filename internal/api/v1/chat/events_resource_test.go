//go:build darwin || linux

package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/ent"
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
)

var titleSubscriptionResource = flag.Bool("title-subscription-resource", false, "measure three real HTTP/SQLCipher slow-title-subscription and reconnect-snapshot rounds")

// HTTP handler 的真实写路径被测试闸门阻塞，以确定性地产生慢订阅。
// 不依赖 OS socket buffer 大小，未冒称真实 TCP 写超时验收。
type titleBlockedWriter struct {
	http.ResponseWriter
	entered, release chan struct{}
	once             sync.Once
}

func (w *titleBlockedWriter) Write(p []byte) (int, error) {
	if bytes.HasPrefix(p, []byte("data: ")) {
		w.once.Do(func() { close(w.entered); <-w.release })
	}
	return w.ResponseWriter.Write(p)
}
func (w *titleBlockedWriter) Flush()                      { w.ResponseWriter.(http.Flusher).Flush() }
func (w *titleBlockedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type titleHTTPResource struct {
	HeapBytes  uint64 `json:"heap_bytes"`
	RSSBytes   uint64 `json:"rss_bytes"`
	FDs        int    `json:"fds"`
	Goroutines int    `json:"goroutines"`
}

func sampleTitleHTTPResource(t *testing.T) titleHTTPResource {
	t.Helper()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	directory := "/dev/fd"
	if runtime.GOOS == "linux" {
		directory = "/proc/self/fd"
	}
	fds, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/bin/ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Fatal(err)
	}
	rss, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return titleHTTPResource{memory.HeapAlloc, rss * 1024, len(fds), runtime.NumGoroutine()}
}

func TestTitleSubscriptionHTTPOverflowAndReconnect(t *testing.T) {
	runTitleSubscriptionHTTP(t, nil)
}

func TestTitleSubscriptionHTTPResources(t *testing.T) {
	if !*titleSubscriptionResource {
		t.Skip("run with -title-subscription-resource using the native SQLCipher wrapper")
	}
	for round := 1; round <= 3; round++ {
		runtime.GC()
		before := sampleTitleHTTPResource(t)
		peak := before
		ran := false
		passed := t.Run(fmt.Sprintf("round_%d", round), func(t *testing.T) {
			ran = true
			runTitleSubscriptionHTTP(t, func() {
				sample := sampleTitleHTTPResource(t)
				peak.HeapBytes = max(peak.HeapBytes, sample.HeapBytes)
				peak.RSSBytes = max(peak.RSSBytes, sample.RSSBytes)
				peak.FDs = max(peak.FDs, sample.FDs)
				peak.Goroutines = max(peak.Goroutines, sample.Goroutines)
			})
		})
		if !ran {
			continue
		}
		runtime.GC()
		after := sampleTitleHTTPResource(t)
		if !passed || after.FDs != before.FDs {
			t.Errorf("title subscription resource mismatch: pass=%t FD=%d/%d", passed, after.FDs, before.FDs)
		}
		encoded, err := json.Marshal(struct {
			Round                         int `json:"round"`
			Before, ObservedPeak, AfterGC titleHTTPResource
		}{round, before, peak, after})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("TITLE_SUBSCRIPTION_HTTP_RESOURCE %s", encoded)
	}
}

func runTitleSubscriptionHTTP(t *testing.T, sample func()) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.DatabaseConfig{Path: filepath.Join(dir, "titles.db"), KeyFile: filepath.Join(dir, "key")}
	if err := os.WriteFile(cfg.KeyFile, []byte(strings.Repeat("ab", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureSQLiteDatabase(); err != nil {
		t.Fatal(err)
	}
	client, err := db.InitSQLite(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	previousClient, previousPublisher := db.EntClient, serviceagent.DefaultTitlePublisher
	publisher := serviceagent.NewTitlePublisher()
	db.EntClient, serviceagent.DefaultTitlePublisher = client, publisher
	t.Cleanup(func() { db.EntClient, serviceagent.DefaultTitlePublisher = previousClient, previousPublisher })
	ids := make([]string, 801)
	builders := make([]*ent.KaguyaConversationCreate, len(ids))
	for i := range ids {
		ids[i] = fmt.Sprintf("title-http-%04d", i)
		builders[i] = client.KaguyaConversation.Create().SetID(ids[i]).SetTitle("已提交标题").SetTitleRevision(3).
			SetModelID("fixture").SetModelName("fixture").SetLastMessageAt(time.Now())
	}
	if err := client.KaguyaConversation.CreateBulk(builders...).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := &ChatApiV1Group{}
	engine.GET("/events", api.ChatEvents)
	engine.POST("/snapshots", api.ConversationTitleSnapshots)
	entered, release, ended := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var active atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active.Add(1)
		defer active.Add(-1)
		if r.URL.Query().Get("slow") == "1" {
			defer close(ended)
			w = &titleBlockedWriter{ResponseWriter: w, entered: entered, release: release}
		}
		engine.ServeHTTP(w, r)
	}))
	transport := &http.Transport{}
	httpClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		publisher.Close()
		transport.CloseIdleConnections()
		server.CloseClientConnections()
		server.Close()
		if active.Load() != 0 {
			t.Error("title HTTP handlers retained after server join")
		}
	})
	response, err := httpClient.Get(server.URL + "/events?slow=1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("title subscription failed")
	}
	publisher.Publish(dtochat.TitleEvent{Type: dtochat.TitleEventUpdated, ConversationID: ids[0], Title: "已提交标题", Revision: 3})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("slow writer did not enter")
	}
	// blocked writer 已取出第一帧；64条填满缓冲，下一条注销订阅。
	for i := 0; i < 65; i++ {
		publisher.Publish(dtochat.TitleEvent{Type: dtochat.TitleEventUpdated, ConversationID: ids[0], Title: "已提交标题", Revision: 3})
	}
	if sample != nil {
		sample()
	}
	// 在断线期间提交最新版本；此事件没有订阅者接收，快照必须补齐。
	if err := client.KaguyaConversation.UpdateOneID(ids[0]).SetTitle("断线后的标题").SetTitleRevision(4).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	publisher.Publish(dtochat.TitleEvent{Type: dtochat.TitleEventUpdated, ConversationID: ids[0], Title: "断线后的标题", Revision: 4})
	releaseOnce.Do(func() { close(release) })
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(body, []byte("data: ")) != 65 || bytes.Contains(body, []byte("断线后的标题")) {
		t.Fatal("overflow did not disconnect at the bounded subscription capacity")
	}
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("overflow handler did not join")
	}
	reconnected, err := httpClient.Get(server.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Body.Close()
	// 请求5个真实快照批次，200/200/200/200/1；包括未加载首屏之外的ID。
	for start := 0; start < len(ids); start += 200 {
		batch := ids[start:min(start+200, len(ids))]
		encoded, err := json.Marshal(dtochat.TitleSnapshotsReq{IDs: batch})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := httpClient.Post(server.URL+"/snapshots", "application/json", bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Code int                        `json:"code"`
			Data dtochat.TitleSnapshotsResp `json:"data"`
		}
		decodeErr := json.NewDecoder(snapshot.Body).Decode(&envelope)
		closeErr := snapshot.Body.Close()
		if decodeErr != nil || closeErr != nil || envelope.Code != dtocode.SystemSuccess.Code || len(envelope.Data.Items) != len(batch) || len(envelope.Data.MissingIDs) != 0 {
			t.Fatalf("snapshot failed: decode=%v close=%v code=%d count=%d", decodeErr, closeErr, envelope.Code, len(envelope.Data.Items))
		}
		for i, item := range envelope.Data.Items {
			wantTitle, wantRevision := "已提交标题", int64(3)
			if item.ID == ids[0] {
				wantTitle, wantRevision = "断线后的标题", 4
			}
			if item.ID != batch[i] || item.Title != wantTitle || item.TitleRevision != wantRevision || item.JobStatus != nil || item.JobRevision != 0 {
				t.Fatalf("snapshot authority mismatch: %+v", item)
			}
		}
	}
	if err := client.KaguyaConversation.UpdateOneID(ids[0]).SetTitle("重连后的标题").SetTitleRevision(5).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	publisher.Publish(dtochat.TitleEvent{Type: dtochat.TitleEventUpdated, ConversationID: ids[0], Title: "重连后的标题", Revision: 5})
	line, err := bufio.NewReader(reconnected.Body).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Code int                `json:"code"`
		Data dtochat.TitleEvent `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != dtocode.SystemSuccess.Code || envelope.Data.Revision != 5 || envelope.Data.Title != "重连后的标题" {
		t.Fatal("reconnected subscription replayed stale data or lost new event")
	}
	if sample != nil {
		sample()
	}
	if count, err := client.KaguyaTitleJob.Query().Count(context.Background()); err != nil || count != 0 {
		t.Fatal("snapshot subscription unexpectedly created title jobs")
	}
	publisher.Close()
	if _, err := io.Copy(io.Discard, reconnected.Body); err != nil {
		t.Fatal(err)
	}
}
