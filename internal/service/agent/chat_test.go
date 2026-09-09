package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
)

type chatTestID struct{ value atomic.Int64 }

func (g *chatTestID) GenID() (int64, error) { return g.value.Add(1), nil }

func setupChatTest(t *testing.T) (context.Context, *ent.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	client, err := ent.Open(dialect.SQLite, fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(1)", t.Name()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldID, oldLogger := db.EntClient, global.Id, global.Logger
	gen := &chatTestID{}
	gen.value.Store(123456789012340)
	db.EntClient, global.Id, global.Logger = client, gen, zap.NewNop()
	t.Cleanup(func() { db.EntClient, global.Id, global.Logger = oldClient, oldID, oldLogger })
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
	var titleCalls atomic.Int64
	titleGate := make(chan struct{})
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
			titleCalls.Add(1)
			select {
			case <-titleGate:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"title","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"会话测试标题"},"finish_reason":"stop"}]}`)
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
	_, err = client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").SetIsDefault(consts.IsTrue).SetIsTask(consts.IsTrue).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run := func(id, prompt string) (string, request) {
		t.Helper()
		ch := make(chan *dtochat.ChatResp)
		go (&AgentSvc{}).Chat(ctx, ch, &dtochat.ChatReq{ID: id, Messages: prompt})
		var done, starts int
		var text strings.Builder
		var returnedID string
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
	// 聊天完成不自动生成标题，必须由客户端显式请求。
	if titleCalls.Load() != 0 {
		t.Fatal("chat automatically generated title")
	}
	initial, err := client.KaguyaConversation.Get(ctx, id)
	if err != nil || initial.Title != defaultConversationTitle {
		t.Fatalf("initial title: %+v %v", initial, err)
	}
	close(titleGate)
	if _, err := (&AgentSvc{}).ConversationTitleGenerate(ctx, id); err != nil {
		t.Fatal(err)
	}
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
	if titleCalls.Load() != 1 {
		t.Fatal("chat generated another title without a request")
	}
	if _, err := (&AgentSvc{}).ConversationTitleGenerate(ctx, other); err != nil {
		t.Fatal(err)
	}
	waitConversationTitle(t, ctx, client, other, "会话测试标题")
	if titleCalls.Load() != 2 {
		t.Fatalf("title calls=%d; only explicit requests should generate titles", titleCalls.Load())
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
