package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyonmu/kaguya/internal/consts"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
)

func TestConversationTitleGenerateRetriesNextTurn(t *testing.T) {
	ctx, client := setupChatTest(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-opencode-session") != "123" {
			t.Error("missing task session header")
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "background-model" {
			t.Errorf("task model = %q", body.Model)
		}
		if len(body.Messages) != 2 || !strings.Contains(body.Messages[1].Content, "你好") || !strings.Contains(body.Messages[1].Content, "回答") || strings.Contains(body.Messages[1].Content, "思考") || strings.Contains(body.Messages[1].Content, "找到") {
			t.Errorf("title input must contain only visible first-turn text: %+v", body)
		}
		if calls.Add(1) == 1 {
			http.Error(w, "provider unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"title","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"重试成功的标题"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("test").SetAPIProtocol(consts.ProtocolOpenAIChat).SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaProviderInfo.UpdateOne(provider).SetProviderType(consts.ProviderTypeOpenCodeGo).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("task").SetModelID("background-model").SetIsTask(consts.IsTrue).SetIsDefault(consts.IsFalse).Save(ctx); err != nil {
		t.Fatal(err)
	}
	svc := &AgentSvc{}
	for version := int64(0); version < 2; version++ {
		turn := testCompletedTurn("123", version)
		// 原聊天提供商不可用，也必须使用独立任务模型。
		turn.ProviderID = "deleted-chat-provider"
		if err := saveCompletedTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
		resp, err := svc.ConversationTitleGenerate(ctx, "123")
		if err != nil {
			t.Fatal(err)
		}
		want := defaultConversationTitle
		if version == 1 {
			want = "重试成功的标题"
		}
		if resp.Title != want || calls.Load() != version+1 {
			t.Fatalf("response=%+v calls=%d", resp, calls.Load())
		}
		stored, err := svc.ConversationDetail(ctx, "123")
		if err != nil || stored.Title != want || stored.TurnCount != version+1 || stored.Usage.TotalTokens != int(35*(version+1)) {
			t.Fatalf("stored title/usage mismatch: %+v %v", stored, err)
		}
	}
	if _, err := svc.ConversationTitleGenerate(ctx, "123"); err != nil {
		t.Fatal(err)
	}
	manual := "用户标题"
	if _, err := svc.ConversationUpdate(ctx, "123", &dtochat.ConversationUpdateReq{Title: &manual}); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.ConversationTitleGenerate(ctx, "123")
	if err != nil || resp.Title != manual || calls.Load() != 2 {
		t.Fatalf("regenerated a completed/manual title: %+v %v calls=%d", resp, err, calls.Load())
	}
}

func TestConversationTitleGenerateSharesPendingTask(t *testing.T) {
	ctx, client := setupChatTest(t)
	var calls atomic.Int64
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"title","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"共享标题"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("test").SetAPIProtocol(consts.ProtocolOpenAIChat).SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("task").SetModelID("task").SetIsTask(consts.IsTrue).Save(ctx); err != nil {
		t.Fatal(err)
	}
	turn := testCompletedTurn("123", 0)
	turn.ProviderID = provider.ID
	if err := saveCompletedTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			resp, err := (&AgentSvc{}).ConversationTitleGenerate(ctx, "123")
			if err == nil && resp.Title != "共享标题" {
				err = fmt.Errorf("unexpected title: %q", resp.Title)
			}
			results <- err
		}()
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-started:
		t.Fatal("duplicate generation")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("generated %d times", calls.Load())
	}
}
