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
	"github.com/lyonmu/kaguya/internal/ent/kaguyatitlejob"
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

// 标题任务通过持久队列执行：等待者广播、取消等待不影响后台、改名/删除不覆盖，
// 且标题调用不写入聊天用量与上下文。
func TestConversationTitleBackgroundResult(t *testing.T) {
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
		if len(body.Messages) == 2 && (!strings.Contains(body.Messages[1].Content, "你好") || !strings.Contains(body.Messages[1].Content, "回答") || strings.Contains(body.Messages[1].Content, "思考") || strings.Contains(body.Messages[1].Content, "找到")) {
			t.Errorf("title input must contain only visible first-turn text: %+v", body)
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"title","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"生成的会话标题"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("title").SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("task").SetModelID("title-model").SetRequestPath("/v1/chat/completions").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetTaskModelID(model.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueConversationTitle(ctx, "123"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("title task did not start")
	}
	// 多个等待者共享完成通知，在任务结束前不返回。
	waits := make(chan error, 2)
	for range 2 {
		go func() {
			_, waitErr := (&AgentSvc{}).ConversationTitleWait(ctx, "123")
			waits <- waitErr
		}()
	}
	select {
	case waitErr := <-waits:
		t.Fatalf("wait returned before title completed: %v", waitErr)
	case <-time.After(20 * time.Millisecond):
	}
	// 请求取消不能终止后台任务。
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := (&AgentSvc{}).ConversationTitleWait(cancelCtx, "123"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait: %v", err)
	}
	close(release)
	for range 2 {
		select {
		case waitErr := <-waits:
			if waitErr != nil {
				t.Fatalf("wait result: %v", waitErr)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	row, err := client.KaguyaConversation.Get(ctx, "123")
	if err != nil || row.Title != "生成的会话标题" || row.TitleRevision != 1 {
		t.Fatalf("title/revision=%q/%d err=%v", row.Title, row.TitleRevision, err)
	}
	job, err := client.KaguyaTitleJob.Query().Where(kaguyatitlejob.ConversationIDEQ("123")).Only(ctx)
	if err != nil || job.Status != kaguyatitlejob.StatusSucceeded {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if row.TurnCount != 1 || row.TotalTokens != 35 || !row.LastMessageAt.Equal(before.LastMessageAt) {
		t.Fatal("title call changed conversation usage/order")
	}
	history, version, err := loadConversation(ctx, "123")
	if err != nil || version != 1 || len(history) != 2 {
		t.Fatalf("title polluted context: %+v %d %v", history, version, err)
	}
	// 手动改名递增 revision 并取消后续自动覆盖。
	manual := "用户自定义标题"
	if _, err := (&AgentSvc{}).ConversationUpdate(ctx, "123", &dtochat.ConversationUpdateReq{Title: &manual}); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueConversationTitle(ctx, "123"); err != nil {
		t.Fatal(err)
	}
	row, err = client.KaguyaConversation.Get(ctx, "123")
	if err != nil || row.Title != manual || row.TitleRevision != 2 {
		t.Fatalf("manual rename: %+v %v", row, err)
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
	_, err := generateConversationTitle(ctx, agentruntime.ProviderConfig{Protocol: consts.ProtocolOpenAIChat, BaseURL: server.URL, RequestPath: "/v1/chat/completions", APIKey: "test", ModelID: "test"}, "问题", "回答")
	if err == nil {
		t.Fatal("expected timeout")
	}
}
