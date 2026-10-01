package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("parallel").SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").SetRequestPath("/v1/chat/completions").Save(ctx)
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

// 聊天没有全局并发上限：超过旧上限（16）的独立会话必须能同时进入模型。
func TestChatHasNoGlobalConcurrencyLimit(t *testing.T) {
	ctx, client := setupChatTest(t)
	ctx, cancelAll := context.WithTimeout(ctx, 30*time.Second)
	defer cancelAll()

	const chats = 17
	entered := make(chan struct{}, chats)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Stream {
			// 标题辅助调用立即返回，不计入并发门。
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"t","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"标题"},"finish_reason":"stop"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"answer\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		case <-release:
			fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer server.Close()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("unbounded").SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").SetRequestPath("/v1/chat/completions").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	streams := make([]chan *dtochat.ChatResp, chats)
	for index := range streams {
		streams[index] = make(chan *dtochat.ChatResp, 128)
		go (&AgentSvc{}).Chat(ctx, streams[index], &dtochat.ChatReq{
			ID: fmt.Sprintf("unbounded-%d", index), ModelID: model.ID, Messages: "hi",
		})
	}
	for index := 0; index < chats; index++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatalf("only %d of %d conversations reached the provider", index, chats)
		}
	}
	close(release)
	released = true
	done := 0
	for _, frames := range streams {
		for frame := range frames {
			if frame.Err != nil {
				t.Fatalf("conversation failed: %v", frame.Err)
			}
			if frame.Chat.Flag == dtochat.ChatFlagDone {
				done++
			}
		}
	}
	if done != chats {
		t.Fatalf("completed=%d of %d", done, chats)
	}
}
