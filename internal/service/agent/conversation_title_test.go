package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	agentruntime "github.com/lyonmu/kaguya/internal/agent/runtime"
	"github.com/lyonmu/kaguya/internal/consts"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"go.uber.org/zap"
)

func TestNormalizeConversationTitle(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"  “数据库性能分析”  ", "数据库性能分析"},
		{"标题\n多余解释", "标题"},
		{"\n \t", ""},
		{strings.Repeat("中", 25), strings.Repeat("中", 20)},
		{strings.Repeat("🙂", 21), strings.Repeat("🙂", 20)},
	} {
		got := normalizeConversationTitle(tt.input)
		if got != tt.want || utf8.RuneCountInString(got) > 20 {
			t.Fatalf("normalize %q = %q", tt.input, got)
		}
	}
}

func TestConversationTitleBackgroundResult(t *testing.T) {
	for _, mode := range []string{"success", "error", "empty", "truncated", "manual", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			ctx, client := setupChatTest(t)
			if err := saveCompletedTurn(ctx, testCompletedTurn("123", 0)); err != nil {
				t.Fatal(err)
			}
			before, err := (&AgentSvc{}).ConversationDetail(ctx, "123")
			if err != nil {
				t.Fatal(err)
			}
			if before.Title != defaultConversationTitle {
				t.Fatalf("default title=%q", before.Title)
			}
			started, release := make(chan struct{}, 1), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Conversation-ID") != "123" {
					t.Error("missing conversation header")
				}
				var body struct {
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
					Stream bool `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Stream || len(body.Messages) != 2 || body.Messages[0].Role != "system" {
					t.Errorf("title call should be independent: %+v", body)
				}
				if len(body.Messages) == 2 {
					if !strings.Contains(body.Messages[1].Content, "首轮问题") || !strings.Contains(body.Messages[1].Content, "首轮回答") {
						t.Errorf("missing first turn: %+v", body)
					}
				}
				started <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				if mode == "error" {
					http.Error(w, "provider unavailable", 500)
					return
				}
				text, reason := strings.Repeat("标题", 15), "stop"
				if mode == "empty" {
					text = "   "
				}
				if mode == "truncated" {
					reason = "length"
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"id":"title","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":%q}]}`, text, reason)
			}))
			defer server.Close()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			cfg := agentruntime.ProviderConfig{Protocol: consts.ProtocolOpenAIChat, BaseURL: server.URL, APIKey: "test", ModelID: "test", ConversationID: "123"}
			result := startConversationTitle(client, zap.NewNop(), cfg, "首轮问题", "首轮回答")
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// 多个等待者收到同一个完成广播，不竞争消费 result channel。
			type waitResult struct {
				resp *dtochat.ConversationTitleResp
				err  error
			}
			waits := make(chan waitResult, 2)
			for range 2 {
				go func() {
					resp, err := (&AgentSvc{}).ConversationTitleWait(ctx, "123")
					waits <- waitResult{resp, err}
				}()
			}
			select {
			case result := <-waits:
				t.Fatalf("wait returned before title completed: %+v", result)
			case <-time.After(20 * time.Millisecond):
			}
			// 取消一个 HTTP 等待不能终止后台生成任务。
			cancelCtx, cancel := context.WithCancel(ctx)
			cancel()
			if _, err := (&AgentSvc{}).ConversationTitleWait(cancelCtx, "123"); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled wait: %v", err)
			}
			if mode == "manual" {
				title := "用户自定义标题"
				if _, err := (&AgentSvc{}).ConversationUpdate(ctx, "123", &dtochat.ConversationUpdateReq{Title: &title}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "deleted" {
				if err := (&AgentSvc{}).ConversationDelete(ctx, "123"); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			var generated conversationTitleResult
			select {
			case generated = <-result:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if _, open := <-result; open {
				t.Fatal("result channel not closed")
			}
			row, err := client.KaguyaConversation.Get(ctx, "123")
			if err != nil {
				t.Fatal(err)
			}
			want := defaultConversationTitle
			switch mode {
			case "success":
				want = strings.Repeat("标题", 10)
				if !generated.Updated || generated.Err != nil {
					t.Fatalf("result=%+v", generated)
				}
			case "manual":
				want = "用户自定义标题"
				if generated.Updated {
					t.Fatal("overwrote manual title")
				}
			case "deleted":
				if generated.Updated || row.DeletedAt == nil {
					t.Fatal("updated deleted conversation")
				}
			default:
				if generated.Err == nil || generated.Updated {
					t.Fatalf("expected fallback: %+v", generated)
				}
			}
			for range 2 {
				select {
				case result := <-waits:
					if mode == "deleted" {
						if !errors.Is(result.err, ErrConversationNotFound) {
							t.Fatalf("deleted wait: %+v", result)
						}
					} else if result.err != nil || result.resp.ID != "123" || result.resp.Title != want {
						t.Fatalf("wait result: %+v, want title %q", result, want)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			conversationTitles.Lock()
			_, pending := conversationTitles.pending["123"]
			conversationTitles.Unlock()
			if pending {
				t.Fatal("completed title task was not removed")
			}
			if row.Title != want {
				t.Fatalf("title=%q want=%q", row.Title, want)
			}
			if row.TurnCount != 1 || row.TotalTokens != 35 || !row.LastMessageAt.Equal(before.LastMessageAt) {
				t.Fatal("title call changed conversation usage/order")
			}
			history, version, err := loadConversation(ctx, "123")
			if mode != "deleted" && (err != nil || version != 1 || len(history) != 2) {
				t.Fatalf("title polluted context: %+v %d %v", history, version, err)
			}
		})
	}
}

func TestGenerateConversationTitleTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := generateConversationTitle(ctx, agentruntime.ProviderConfig{Protocol: consts.ProtocolOpenAIChat, BaseURL: server.URL, APIKey: "test", ModelID: "test"}, "问题", "回答")
	if err == nil {
		t.Fatal("expected timeout")
	}
}
