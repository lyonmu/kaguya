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

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/consts"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
)

// 失败或取消的轮次保留已产生的正文，但绝不进入续聊上下文、用量统计或完整轮次。
func TestChatIncompleteTurnKeepsPartialContent(t *testing.T) {
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
					if frame.Chat.Flag == dtochat.ChatFlagDone {
						done++
					}
					if frame.Err != nil {
						fail++
					}
					if mode == "cancel" && frame.Chat.Block != nil && frame.Chat.Block.Text != "" {
						// 断联/停止：取消请求上下文，服务端应停止生成并保留已推送内容。
						cancel()
					}
				}
				cancel()
				// 取消时连接已断开，错误帧不再投递；其余失败路径必须上报错误。
				if done != 0 || (mode != "cancel" && fail != 1) {
					t.Fatalf("done=%d failure=%d", done, fail)
				}
			}
			if requests.Load() != 2 {
				t.Fatalf("requests=%d", requests.Load())
			}
			// 续聊上下文只读取完整提交的轮次，但会拼回未完成轮次的用户提问；
			// 半截助手内容不能进入上下文。
			after, version, err := loadConversation(ctx, "123")
			if err != nil {
				t.Fatal(err)
			}
			afterJSON, _ := json.Marshal(after)
			want := append(append([]fantasy.Message{}, before...), fantasy.NewUserMessage("must not save"))
			wantJSON, _ := json.Marshal(want)
			if version != 1 || string(afterJSON) != string(wantJSON) {
				t.Fatalf("incomplete request context=%s want=%s", afterJSON, wantJSON)
			}
			detail, err := (&AgentSvc{}).ConversationDetail(ctx, "123")
			if err != nil || detail.Usage.TotalTokens != 35 || detail.TurnCount != 1 {
				t.Fatalf("usage changed: %+v %v", detail, err)
			}
			// 失败的新会话保留在列表中，且没有完整轮次。
			if n, err := client.KaguyaConversation.Query().Count(ctx); err != nil || n != 2 {
				t.Fatalf("conversations=%d %v", n, err)
			}
			if n, err := client.KaguyaConversation.Query().Where(kaguyaconversation.TurnCountEQ(0)).Count(ctx); err != nil || n != 1 {
				t.Fatalf("empty conversations=%d %v", n, err)
			}
			// 中断轮次可展示：状态正确，且有部分内容时可回放。
			failed, err := client.KaguyaChatTurn.Query().Where(
				kaguyachatturn.ConversationIDEQ("123"), kaguyachatturn.StatusNEQ(kaguyachatturn.StatusCompleted)).Only(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus := kaguyachatturn.StatusFailed
			if mode == "cancel" {
				wantStatus = kaguyachatturn.StatusInterrupted
			}
			if failed.Status != wantStatus {
				t.Fatalf("status=%s want=%s", failed.Status, wantStatus)
			}
			blocks, err := failed.QueryBlocks().All(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "error" {
				if len(blocks) != 0 {
					t.Fatalf("provider error before content wrote blocks: %+v", blocks)
				}
			} else if mode != "storage" {
				if len(blocks) == 0 || blocks[0].Text != "partial" {
					t.Fatalf("partial content lost: %+v", blocks)
				}
			}
			// 展示接口把中断轮次作为历史返回，正文可读。
			page, err := (&AgentSvc{}).ConversationTurns(ctx, "123", &dtochat.TurnPageReq{Limit: 5, Compact: true})
			if err != nil || len(page.Items) != 2 || page.Items[1].Status != string(wantStatus) {
				t.Fatalf("turn page=%+v %v", page, err)
			}
			if mode != "error" && mode != "storage" && (len(page.Items[1].Blocks) == 0 || page.Items[1].Blocks[0].Text != "partial") {
				t.Fatalf("history lost partial content: %+v", page.Items[1])
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
		if frame.Chat.Flag == dtochat.ChatFlagDone {
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
