package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	"github.com/lyonmu/kaguya/internal/global"
	initialize "github.com/lyonmu/kaguya/internal/init"
	"go.uber.org/zap"
)

type chatTestID struct{ value atomic.Int64 }

func (g *chatTestID) GenID() (int64, error) { return g.value.Add(1), nil }

func setupChatTest(t *testing.T) (context.Context, *ent.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	// 测试库是 shared-cache 的内存 SQLite：表级锁不会等待 busy handler，而是
	// 直接返回 SQLITE_LOCKED。限制单连接串行写入，避免标题任务与聊天事务
	// 并发写同一张表时的随机失败；生产 PostgreSQL 不受影响。
	conn, err := sql.Open(dialect.SQLite, fmt.Sprintf("file:%s?mode=memory&cache=shared&_foreign_keys=on&_busy_timeout=5000", t.Name()))
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, conn)))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldID, oldLogger := db.EntClient, global.Id, global.Logger
	gen := &chatTestID{}
	gen.value.Store(123456789012340)
	db.EntClient, global.Id, global.Logger = client, gen, zap.NewNop()
	t.Cleanup(func() { db.EntClient, global.Id, global.Logger = oldClient, oldID, oldLogger })
	if err := initialize.Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	// Tests must not load the developer machine's global instructions.
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetGlobalAgentsPaths([]string{}).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	return ctx, client
}

// 真正走 Service → Agent → HTTP，验证唯一 done、落库和从数据库恢复历史。
func TestChatConversationAndSingleDone(t *testing.T) {
	ctx, client := setupChatTest(t)
	type request struct {
		id       string
		messages json.RawMessage
	}
	requests := make(chan request, 3)
	titleCalls := make(chan struct{}, 4)
	titleGate := make(chan struct{})
	titleReady := make(chan struct{})
	var titleOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages json.RawMessage `json:"messages"`
			Stream   bool            `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if !body.Stream {
			// 首轮标题只用用户提问，不等助手回答。
			if strings.Contains(string(body.Messages), `"answer"`) {
				t.Errorf("first-turn title must not wait for the answer: %s", body.Messages)
			}
			titleCalls <- struct{}{}
			titleOnce.Do(func() { close(titleReady) })
			select {
			case <-titleGate:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"title","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"会话测试标题"},"finish_reason":"stop"}]}`)
			return
		}
		// 正文请求必须与标题任务并行：标题调用先发生，正文才继续。
		select {
		case <-titleReady:
		case <-r.Context().Done():
			t.Error("chat turn did not start the title task")
			return
		}
		requests <- request{r.Header.Get("X-Conversation-ID"), body.Messages}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"answer\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	defer func() {
		select {
		case <-titleGate:
		default:
			close(titleGate)
		}
	}()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("test").SetAPIProtocol(consts.ProtocolOpenAIChat).SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetDefaultModelID(model.ID).SetTaskModelID(model.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	run := func(id, prompt string) (string, request) {
		t.Helper()
		ch := make(chan *dtochat.ChatResp)
		go (&AgentSvc{}).Chat(ctx, ch, &dtochat.ChatReq{ID: id, Messages: prompt})
		var done, starts int
		var text strings.Builder
		var returnedID string
		var startErr error
		for frame := range ch {
			if frame.Err != nil {
				t.Fatal(frame.Err)
			}
			if returnedID == "" {
				returnedID = frame.Chat.ID
			}
			if frame.Chat.ID != returnedID {
				t.Fatalf("conversation ID changed: %+v", frame.Chat)
			}
			switch frame.Chat.Flag {
			case dtochat.WSFlagStart:
				starts++
				// 首轮在正文开始生成前已落库，不再等待轮次完成；
				// 记录错误而不是直接终止，避免 Chat goroutine 访问已回收的全局状态。
				if _, err := client.KaguyaConversation.Get(ctx, returnedID); err != nil {
					startErr = fmt.Errorf("conversation missing at start frame: %w", err)
				}
			case dtochat.WSFlagDone:
				done++
				if frame.Chat.Content != "" || frame.Chat.Block != nil {
					t.Fatalf("done repeated content: %+v", frame.Chat)
				}
			}
			if b := frame.Chat.Block; b != nil && b.Type == dtochat.BlockTypeText {
				text.WriteString(b.Text)
				if b.Phase == dtochat.BlockPhaseEnd && b.Text != "" {
					t.Fatal("block_end repeated text")
				}
			}
		}
		if startErr != nil {
			t.Fatal(startErr)
		}
		if starts != 1 || done != 1 || text.String() != "answer" {
			t.Fatalf("starts=%d done=%d text=%q", starts, done, text.String())
		}
		select {
		case req := <-requests:
			if req.id != returnedID {
				t.Fatalf("upstream ID=%q response ID=%q", req.id, returnedID)
			}
			return returnedID, req
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return "", request{}
		}
	}
	id, _ := run("", "first-question")
	// 首轮开始时就并行生成标题：标题调用早于正文结束。
	select {
	case <-titleCalls:
	case <-ctx.Done():
		t.Fatal("chat turn did not request a title")
	}
	// 标题模型调用仍被测试闸门阻塞，因此此刻仍是默认标题。
	initial, err := client.KaguyaConversation.Get(ctx, id)
	if err != nil || initial.Title != defaultConversationTitle {
		t.Fatalf("initial title: %+v %v", initial, err)
	}
	close(titleGate)
	waitConversationTitle(t, ctx, client, id, "会话测试标题")
	resumed, req := run(id, "second-question")
	count, err := client.KaguyaConversation.Query().Count(ctx)
	if err != nil || resumed != id || count != 1 {
		t.Fatalf("resuming created a new conversation: count=%d err=%v", count, err)
	}
	detail, err := (&AgentSvc{}).ConversationDetail(ctx, id)
	if err != nil || detail.TurnCount != 2 {
		t.Fatalf("missing completed turns: %+v %v", detail, err)
	}
	for _, text := range []string{"first-question", "answer", "second-question"} {
		if !strings.Contains(string(req.messages), text) {
			t.Fatalf("history missing %q: %s", text, req.messages)
		}
	}
	other, req := run("", "independent-question")
	if other == id || strings.Contains(string(req.messages), "first-question") {
		t.Fatal("new conversation reused old history")
	}
	// 续聊不重复生成标题；新会话只生成一次，且不依赖客户端请求。
	select {
	case <-titleCalls:
	case <-ctx.Done():
		t.Fatal("new conversation did not request a title")
	}
	select {
	case <-titleCalls:
		t.Fatal("chat generated titles more than once per conversation")
	case <-time.After(20 * time.Millisecond):
	}
	title, err := (&AgentSvc{}).ConversationTitleGenerate(ctx, other)
	if err != nil || title.Title != "会话测试标题" {
		t.Fatalf("stored title: %+v %v", title, err)
	}
	waitConversationTitle(t, ctx, client, other, "会话测试标题")
}

// 首轮失败时仍保留已创建的会话与生成的标题：会话在正文开始前落库，
// 标题任务只依赖用户提问，不再等待轮次提交。
func TestChatFailureKeepsStartedConversationAndTitle(t *testing.T) {
	ctx, client := setupChatTest(t)
	titleCalled := make(chan struct{})
	var titleOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if !body.Stream {
			titleOnce.Do(func() { close(titleCalled) })
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"title","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"失败会话标题"},"finish_reason":"stop"}]}`)
			return
		}
		// 等标题任务已经调用模型，再让本轮失败。
		select {
		case <-titleCalled:
		case <-r.Context().Done():
			t.Error("chat turn did not start the title task")
			return
		}
		http.Error(w, "chat provider unavailable", http.StatusInternalServerError)
	}))
	defer server.Close()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("test").SetAPIProtocol(consts.ProtocolOpenAIChat).SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetDefaultModelID(model.ID).SetTaskModelID(model.ID).SetChatMaxRetries(0).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	ch := make(chan *dtochat.ChatResp)
	go (&AgentSvc{}).Chat(ctx, ch, &dtochat.ChatReq{Messages: "失败问题"})
	var convID string
	var failed bool
	for frame := range ch {
		if frame.Chat.ID != "" {
			convID = frame.Chat.ID
		}
		if frame.Chat.Flag == dtochat.WSFlagDone {
			t.Fatal("failed turn reported done")
		}
		if frame.Err != nil {
			failed = true
		}
	}
	if !failed {
		t.Fatal("expected the turn to fail")
	}
	if convID == "" {
		t.Fatal("failure frame lost the conversation ID")
	}
	// 会话在首轮开始时已落库；标题任务完成后写入标题并移出 pending。
	waitConversationTitle(t, ctx, client, convID, "失败会话标题")
	deadline := time.Now().Add(2 * time.Second)
	for {
		conversationTitles.Lock()
		pending := conversationTitles.pending[convID]
		conversationTitles.Unlock()
		if pending == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("title task stayed pending")
		}
		time.Sleep(5 * time.Millisecond)
	}
	row, err := client.KaguyaConversation.Get(ctx, convID)
	if err != nil || row.TurnCount != 0 {
		t.Fatalf("failed turn persisted a turn: %+v %v", row, err)
	}
	if count, err := client.KaguyaConversation.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("failed turn conversation count=%d err=%v", count, err)
	}
}

func waitConversationTitle(t *testing.T, ctx context.Context, client *ent.Client, id, want string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		row, err := client.KaguyaConversation.Get(ctx, id)
		if err == nil && row.Title == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for title %q: %v", want, ctx.Err())
		case <-ticker.C:
		}
	}
}
