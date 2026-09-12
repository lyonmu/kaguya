package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lyonmu/kaguya/internal/consts"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
)

// Both providers must be entered before either is released: this detects accidental
// global serialization in the actual Chat execution path, not only in its lock.
func TestChatIndependentConversationsRunConcurrently(t *testing.T) {
	ctx, client := setupChatTest(t)
	ctx, cancelAll := context.WithTimeout(ctx, 10*time.Second)
	defer cancelAll()
	entered := make(chan struct{}, 2)
	finish := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"answer\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		case <-finish:
			fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer server.Close()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("parallel").SetAPIProtocol(consts.ProtocolOpenAIChat).SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	firstCtx, cancelFirst := context.WithCancel(ctx)
	defer cancelFirst()
	first, second := make(chan *dtochat.ChatResp, 64), make(chan *dtochat.ChatResp, 64)
	go (&AgentSvc{}).Chat(firstCtx, first, &dtochat.ChatReq{ID: "parallel-first", ModelID: model.ID, Messages: "first"})
	go (&AgentSvc{}).Chat(ctx, second, &dtochat.ChatReq{ID: "parallel-second", ModelID: model.ID, Messages: "second"})
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("both conversations did not reach the provider concurrently")
		}
	}
	cancelFirst()
	for frame := range first {
		if frame.Chat.Flag == dtochat.ChatFlagDone {
			t.Fatal("cancelled conversation completed")
		}
	}
	close(finish)
	done := false
	for frame := range second {
		if frame.Err != nil {
			t.Fatalf("second conversation failed: %v", frame.Err)
		}
		if frame.Chat.Flag == dtochat.ChatFlagDone {
			done = true
		}
	}
	if !done {
		t.Fatal("cancelling first conversation prevented second from completing")
	}
	if _, version, err := loadConversation(ctx, "parallel-first"); err != nil || version != 0 {
		t.Fatalf("cancelled history: version=%d err=%v", version, err)
	}
	if _, version, err := loadConversation(ctx, "parallel-second"); err != nil || version != 1 {
		t.Fatalf("completed history: version=%d err=%v", version, err)
	}
}
