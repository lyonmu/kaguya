package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lyonmu/kaguya/internal/consts"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
)

func TestChatIncompleteTurnDoesNotPersist(t *testing.T) {
	for _, mode := range []string{"error", "disconnect", "cancel", "length", "storage"} {
		t.Run(mode, func(t *testing.T) {
			ctx, client := setupChatTest(t)
			if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetChatMaxRetries(0).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			if err := saveCompletedTurn(ctx, testCompletedTurn("123", 0)); err != nil {
				t.Fatal(err)
			}
			before, _, err := loadConversation(ctx, "123")
			if err != nil {
				t.Fatal(err)
			}
			beforeJSON, _ := json.Marshal(before)
			if mode == "storage" {
				client.KaguyaChatBlock.Use(func(ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(context.Context, ent.Mutation) (ent.Value, error) { return nil, errors.New("storage failure") })
				})
			}
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if mode == "error" {
					http.Error(w, "provider error", 500)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"},\"finish_reason\":null}]}\n\n")
				w.(http.Flusher).Flush()
				if mode == "cancel" {
					<-r.Context().Done()
					return
				}
				if mode == "length" || mode == "storage" {
					reason := "length"
					if mode == "storage" {
						reason = "stop"
					}
					fmt.Fprintf(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", reason)
				}
				// disconnect: 仅有部分内容，直接 EOF，没有正常 finish。
			}))
			defer server.Close()
			p, err := client.KaguyaProviderInfo.Create().SetProviderName("test").SetAPIProtocol(consts.ProtocolOpenAIChat).SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
			if err != nil {
				t.Fatal(err)
			}
			model, err := client.KaguyaModelsInfo.Create().SetProviderID(p.ID).SetModelName("test").SetModelID("test").Save(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetDefaultModelID(model.ID).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"123", ""} {
				callCtx, cancel := context.WithCancel(ctx)
				ch := make(chan *dtochat.ChatResp)
				go (&AgentSvc{}).Chat(callCtx, ch, &dtochat.ChatReq{ID: id, Messages: "must not save"})
				var done, fail int
				for frame := range ch {
					if frame.Chat.Flag == dtochat.WSFlagDone {
						done++
					}
					if frame.Err != nil {
						fail++
					}
					if mode == "cancel" && frame.Chat.Block != nil && frame.Chat.Block.Text != "" {
						cancel()
					}
				}
				cancel()
				if done != 0 || (mode != "cancel" && fail != 1) {
					t.Fatalf("done=%d failure=%d", done, fail)
				}
			}
			if requests.Load() != 2 {
				t.Fatalf("requests=%d", requests.Load())
			}
			after, version, err := loadConversation(ctx, "123")
			if err != nil {
				t.Fatal(err)
			}
			afterJSON, _ := json.Marshal(after)
			if version != 1 || string(afterJSON) != string(beforeJSON) {
				t.Fatal("incomplete request changed context")
			}
			// 失败的新会话保留在列表中（尚无轮次），已有会话的上下文不受影响。
			if n, err := client.KaguyaConversation.Query().Count(ctx); err != nil || n != 2 {
				t.Fatalf("conversations=%d %v", n, err)
			}
			if n, err := client.KaguyaConversation.Query().Where(kaguyaconversation.TurnCountEQ(0)).Count(ctx); err != nil || n != 1 {
				t.Fatalf("empty conversations=%d %v", n, err)
			}
			if n, err := client.KaguyaChatTurn.Query().Count(ctx); err != nil || n != 1 {
				t.Fatalf("turns=%d %v", n, err)
			}
			if n, err := client.KaguyaChatBlock.Query().Count(ctx); err != nil || n != 3 {
				t.Fatalf("blocks=%d %v", n, err)
			}
			detail, err := (&AgentSvc{}).ConversationDetail(ctx, "123")
			if err != nil || detail.Usage.TotalTokens != 35 {
				t.Fatalf("usage changed: %+v %v", detail, err)
			}
		})
	}
}

func TestChatRetriesTransientStreamOverload(t *testing.T) {
	ctx, client := setupChatTest(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if call == 1 {
			fmt.Fprint(w, "data: {\"error\":{\"type\":\"service_unavailable_error\",\"code\":\"server_is_overloaded\",\"message\":\"overloaded\"}}\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"recovered\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	p, err := client.KaguyaProviderInfo.Create().SetProviderName("retry").SetAPIProtocol(consts.ProtocolOpenAIChat).SetAPIKey("test").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(p.ID).SetModelName("retry").SetModelID("retry").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetDefaultModelID(model.ID).SetChatMaxRetries(1).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	ch := make(chan *dtochat.ChatResp)
	go (&AgentSvc{}).Chat(ctx, ch, &dtochat.ChatReq{Messages: "retry overload"})
	var done, failures int
	var failure error
	var text strings.Builder
	for frame := range ch {
		if frame.Err != nil {
			failures++
			failure = frame.Err
		}
		if frame.Chat.Flag == dtochat.WSFlagDone {
			done++
		}
		if frame.Chat.Block != nil && frame.Chat.Block.Type == dtochat.BlockTypeText {
			text.WriteString(frame.Chat.Block.Text)
		}
	}
	if requests.Load() != 2 || done != 1 || failures != 0 || text.String() != "recovered" {
		t.Fatalf("requests=%d done=%d failures=%d text=%q err=%v", requests.Load(), done, failures, text.String(), failure)
	}
}
