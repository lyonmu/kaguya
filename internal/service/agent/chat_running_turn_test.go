package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lyonmu/kaguya/internal/consts"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
)

// chatProviderServer 返回一个可控制节奏的 SSE 供应商：先发一段首块，等待 release 后再正常结束。
func chatProviderServer(t *testing.T, first string, started chan<- struct{}, release <-chan struct{}, finishReason string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(400)
			return
		}
		if !body.Stream {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"title","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"中途标题"},"finish_reason":"stop"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(first)
		fmt.Fprintf(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%s},\"finish_reason\":null}]}\n\n", chunk)
		w.(http.Flusher).Flush()
		if started != nil {
			started <- struct{}{}
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprintf(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", finishReason)
	}))
}

func configureRunningChatProvider(t *testing.T, ctx context.Context, client *ent.Client, baseURL string) {
	t.Helper()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("running").SetAPIProtocol(consts.ProtocolOpenAIChat).SetAPIKey("test").SetBaseURL(baseURL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("running").SetModelID("running").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetDefaultModelID(model.ID).SetChatMaxRetries(0).Exec(ctx); err != nil {
		t.Fatal(err)
	}
}

// SSE 断联就是本轮生命周期的终点：停止生成、保留断开前已推送的内容并标 interrupted。
// 切换会话不会关闭 SSE，因此轮次继续执行到 completed（见并行会话测试）。
func TestClientDisconnectStopsTurnAndKeepsPartial(t *testing.T) {
	ctx, client := setupChatTest(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server := chatProviderServer(t, "answer", started, release, "stop")
	defer server.Close()
	configureRunningChatProvider(t, ctx, client, server.URL)

	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := make(chan *dtochat.ChatResp, 16)
	go (&AgentSvc{}).Chat(callCtx, ch, &dtochat.ChatReq{Messages: "stop me"})
	<-started

	var convID string
	for frame := range ch {
		if frame.Chat.ID != "" {
			convID = frame.Chat.ID
		}
		if frame.Chat.Flag == dtochat.WSFlagDone {
			t.Fatal("disconnected turn reported done")
		}
		// 收到首块后模拟断网/休眠：取消请求上下文，不再读取后续帧。
		if frame.Chat.Block != nil && frame.Chat.Block.Text != "" && convID != "" {
			cancel()
		}
	}
	turn, err := client.KaguyaChatTurn.Query().Where(kaguyachatturn.ConversationIDEQ(convID)).Only(ctx)
	if err != nil || turn.Status != kaguyachatturn.StatusInterrupted {
		t.Fatalf("turn=%+v err=%v", turn, err)
	}
	blocks, err := turn.QueryBlocks().All(ctx)
	if err != nil || len(blocks) != 1 || blocks[0].Text != "answer" {
		t.Fatalf("partial content lost: %+v err=%v", blocks, err)
	}
	// 中断轮次不进入续聊上下文。
	if _, version, err := loadConversation(ctx, convID); err != nil || version != 0 {
		t.Fatalf("interrupted turn leaked into history: version=%d err=%v", version, err)
	}
	close(release)
}

// 进行中轮次按阈值增量落库，进程中断也不会丢失已产生的内容。
func TestTurnRecorderFlushesWhileRunning(t *testing.T) {
	ctx, client := setupChatTest(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	big := strings.Repeat("x", turnFlushMinBytes+1)
	server := chatProviderServer(t, big, started, release, "stop")
	defer server.Close()
	configureRunningChatProvider(t, ctx, client, server.URL)

	ch := make(chan *dtochat.ChatResp, 16)
	go (&AgentSvc{}).Chat(ctx, ch, &dtochat.ChatReq{Messages: "flush me"})
	<-started

	var turnID string
	deadline := time.Now().Add(10 * time.Second)
	for {
		row, err := client.KaguyaChatTurn.Query().Where(kaguyachatturn.StatusEQ(kaguyachatturn.StatusRunning)).Only(ctx)
		if err == nil {
			blocks, blockErr := row.QueryBlocks().All(ctx)
			if blockErr == nil && len(blocks) == 1 && strings.HasPrefix(blocks[0].Text, "xxx") {
				turnID = row.ID
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("running turn was not flushed: err=%v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// 进行中的半截轮次不能作为续聊上下文。
	if _, version, err := loadConversation(ctx, turnID); err != nil || version != 0 {
		t.Fatalf("running turn leaked into history: version=%d err=%v", version, err)
	}
	close(release)
	for range ch {
	}
	row, err := client.KaguyaChatTurn.Get(ctx, turnID)
	if err != nil || row.Status != kaguyachatturn.StatusCompleted {
		t.Fatalf("turn=%+v err=%v", row, err)
	}
	blocks, err := row.QueryBlocks().All(ctx)
	if err != nil || len(blocks) != 1 || blocks[0].Text != big {
		t.Fatalf("completed blocks=%d err=%v", len(blocks), err)
	}
}

// 用户主动停止与断联都会取消生成，但落库状态区分为 canceled 与 interrupted。
func TestUserStopMarksTurnCanceled(t *testing.T) {
	ctx, client := setupChatTest(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server := chatProviderServer(t, "answer", started, release, "stop")
	defer server.Close()
	configureRunningChatProvider(t, ctx, client, server.URL)

	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := make(chan *dtochat.ChatResp, 16)
	go (&AgentSvc{}).Chat(callCtx, ch, &dtochat.ChatReq{Messages: "stop me please"})
	<-started

	var convID string
	for frame := range ch {
		if frame.Chat.ID != "" {
			convID = frame.Chat.ID
		}
		if frame.Chat.Block != nil && frame.Chat.Block.Text != "" && convID != "" {
			// 先标记用户主动停止，再取消上下文，落库应记为 canceled。
			(&AgentSvc{}).StopConversation(convID)
			cancel()
		}
	}
	turn, err := client.KaguyaChatTurn.Query().Where(kaguyachatturn.ConversationIDEQ(convID)).Only(ctx)
	if err != nil || turn.Status != kaguyachatturn.StatusCanceled {
		t.Fatalf("turn=%+v err=%v", turn, err)
	}
	blocks, err := turn.QueryBlocks().All(ctx)
	if err != nil || len(blocks) != 1 || blocks[0].Text != "answer" {
		t.Fatalf("partial content lost: %+v err=%v", blocks, err)
	}
	if _, version, err := loadConversation(ctx, convID); err != nil || version != 0 {
		t.Fatalf("canceled turn leaked into history: version=%d err=%v", version, err)
	}
	close(release)
}

// 崩溃/强杀后遗留的 running 轮次在下次启动时标记为 interrupted。
func TestReconcileRunningTurnsMarksInterrupted(t *testing.T) {
	ctx, client := setupChatTest(t)
	row, err := beginTurn(ctx, turnStart{ConversationID: "stale", UserContent: "遗留问题", StartedAt: time.Now(), ProviderID: "p", ProviderName: "p", ModelID: "m", ModelName: "m", APIProtocol: "openai-chat"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ReconcileRunningTurns(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := client.KaguyaChatTurn.Get(ctx, row.ID)
	if err != nil || got.Status != kaguyachatturn.StatusInterrupted {
		t.Fatalf("turn=%+v err=%v", got, err)
	}
}

// 中断轮次占用 turn_index，后续轮次在其之后递增，历史读取仍只信任 completed。
func TestBeginTurnAllocatesIndexAfterInterrupted(t *testing.T) {
	ctx, client := setupChatTest(t)
	if err := saveCompletedTurn(ctx, testCompletedTurn("c", 0)); err != nil {
		t.Fatal(err)
	}
	start := turnStart{ConversationID: "c", UserContent: "中断问题", StartedAt: time.Now(), ProviderID: "p", ProviderName: "p", ModelID: "m", ModelName: "m", APIProtocol: "openai-chat"}
	interrupted, err := beginTurn(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	if err := markTurnEnd(interrupted.ID, kaguyachatturn.StatusInterrupted, time.Now()); err != nil {
		t.Fatal(err)
	}
	next := start
	next.UserContent = "下一轮"
	row, err := beginTurn(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	if row.TurnIndex != 3 {
		t.Fatalf("turn_index=%d want 3", row.TurnIndex)
	}
	if _, version, err := loadConversation(ctx, "c"); err != nil || version != 1 {
		t.Fatalf("history version=%d err=%v", version, err)
	}
	if n, err := client.KaguyaChatTurn.Query().Where(kaguyachatturn.StatusEQ(kaguyachatturn.StatusCompleted)).Count(ctx); err != nil || n != 1 {
		t.Fatalf("completed turns=%d err=%v", n, err)
	}
}
