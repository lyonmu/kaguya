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

// 真正走 Service → Agent → HTTP，验证唯一 done、会话 ID 透传和按 ID 恢复历史。
func TestChatConversationAndSingleDone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := ent.Open(dialect.SQLite, "file:chat-flow?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldID, oldLogger, oldStore := db.EntClient, global.Id, global.Logger, conversationStoreInstance
	gen := &chatTestID{}
	gen.value.Store(123456789012340)
	db.EntClient, global.Id, global.Logger, conversationStoreInstance = client, gen, zap.NewNop(), newConversationStore()
	defer func() {
		db.EntClient, global.Id, global.Logger, conversationStoreInstance = oldClient, oldID, oldLogger, oldStore
	}()
	type request struct {
		id       string
		messages json.RawMessage
	}
	requests := make(chan request, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		requests <- request{r.Header.Get("X-Conversation-ID"), body.Messages}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"answer\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("test").SetAPIProtocol(consts.ProtocolOpenAIChat).SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").SetIsDefault(consts.IsTrue).Save(ctx)
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
	count := gen.value.Load()
	resumed, req := run(id, "second-question")
	if resumed != id || gen.value.Load() != count {
		t.Fatal("resuming generated a new ID")
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
}
