package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lyonmu/kaguya/internal/consts"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
)

func TestChatSelectedModel(t *testing.T) {
	ctx, client := setupChatTest(t)
	requests := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- body.Model
		if r.Header.Get("x-opencode-session") == "" || r.Header.Get("x-opencode-session") != r.Header.Get("X-Conversation-ID") {
			t.Error("missing chat session header")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("selected").SetProviderType(consts.ProviderTypeOpenCodeGo).SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	def, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("default").SetModelID("default-api").SetIsDefault(consts.IsTrue).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("selected").SetModelID("selected-api").SetIsDefault(consts.IsFalse).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run := func(id, modelID, want string) string {
		t.Helper()
		ch := make(chan *dtochat.ChatResp)
		go (&AgentSvc{}).Chat(ctx, ch, &dtochat.ChatReq{ID: id, ModelID: modelID, Messages: "hello"})
		done, failed := false, false
		for frame := range ch {
			if frame.Err != nil {
				failed = true
			}
			if frame.Chat.Flag == dtochat.WSFlagDone {
				done = true
				id = frame.Chat.ID
			}
			if frame.Chat.Flag == dtochat.WSFlagStart && frame.ModelID != want {
				t.Errorf("model = %q, want %q", frame.ModelID, want)
			}
		}
		if want == "" {
			if !failed || done {
				t.Fatal("invalid model silently fell back")
			}
			select {
			case <-requests:
				t.Fatal("invalid model reached upstream")
			default:
			}
			return id
		}
		if failed || !done {
			t.Fatal("chat failed")
		}
		select {
		case got := <-requests:
			if got != want {
				t.Errorf("upstream model = %q", got)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		return id
	}
	id := run("", selected.ID, "selected-api")
	run(id, selected.ID, "selected-api")
	run("", "", "default-api")
	run("", "unknown", "")
	if err := client.KaguyaModelsInfo.UpdateOne(selected).SetDeletedAt(time.Now()).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	run("", selected.ID, "")
	if err := client.KaguyaProviderInfo.UpdateOne(provider).SetDeletedAt(time.Now()).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	run("", def.ID, "")
	run("", "", "")
}

func TestTitleRequiresTaskModel(t *testing.T) {
	ctx, client := setupChatTest(t)
	if err := saveCompletedTurn(ctx, testCompletedTurn("123", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := (&AgentSvc{}).ConversationTitleGenerate(ctx, "123"); err == nil {
		t.Fatal("missing task model should fail explicitly")
	}
	row, err := client.KaguyaConversation.Get(ctx, "123")
	if err != nil || row.Title != defaultConversationTitle {
		t.Fatalf("title changed: %+v %v", row, err)
	}
}
