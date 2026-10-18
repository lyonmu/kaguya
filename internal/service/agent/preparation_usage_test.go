package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openai"
	token "github.com/lyonmu/kaguya/internal/agent/token"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
)

func TestFailedReferencePreservesPreparationUsage(t *testing.T) {
	ctx, projectID, _ := seedReferenceProject(t)
	client := db.EntClient
	var calls, streamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Stream {
			streamCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"s","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"Previous work summarized."},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":12,"total_tokens":112}}`)
	}))
	defer server.Close()
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("summary-test").SetAPIKey("fake").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").SetRequestPath("/v1/chat/completions").SetTokenContextWindow(10000).SetTokenMaxOutputTokens(1000).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetChatMaxRetries(0).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	saved := testCompletedTurn("reference-failure", 0)
	saved.ProjectID, saved.ProviderID = projectID, provider.ID
	saved.Messages = []fantasy.Message{fantasy.NewUserMessage(strings.Repeat("old ", 9000)), testAssistantMessage("old answer")}
	empty := ""
	saved.ProjectInstructions = &empty
	saved.InstructionsFormat = "split"
	if err := saveCompletedTurn(ctx, saved); err != nil {
		t.Fatal(err)
	}
	frames := make(chan *dtochat.ChatResp)
	go (&AgentSvc{}).Chat(ctx, frames, &dtochat.ChatReq{ID: saved.ConversationID, ModelID: model.ID, Messages: "inspect missing file", References: []dtochat.Reference{{Type: "file", Path: "missing.txt"}}})
	var got error
	started := false
	for frame := range frames {
		if frame.Chat.Flag == dtochat.ChatFlagStart {
			started = true
		}
		if frame.Chat.Flag == dtochat.ChatFlagDone {
			t.Fatal("reference failure emitted done")
		}
		if frame.Err != nil {
			got = frame.Err
		}
	}
	if !started || !errors.Is(got, ErrReferenceReadFailure) || calls.Load() == 0 || streamCalls.Load() != 0 {
		t.Fatalf("preparation order/frames incorrect: started=%v calls=%d stream=%d err=%v", started, calls.Load(), streamCalls.Load(), got)
	}
	failed, err := client.KaguyaChatTurn.Query().Where(kaguyachatturn.ConversationIDEQ(saved.ConversationID), kaguyachatturn.StatusEQ(kaguyachatturn.StatusFailed)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	audit := failed.PreparationUsage
	if audit == nil || audit.Version != 1 || audit.SummaryCalls != int(calls.Load()) || audit.ReportedCalls != audit.SummaryCalls || audit.UnreportedCalls != 0 || audit.Usage.TotalTokens != int64(calls.Load())*112 {
		t.Fatalf("known summary consumption lost: %+v", audit)
	}
	if failed.UserContent != "inspect missing file" || failed.ContextMessages != nil || failed.ContextFingerprint != nil || failed.TotalTokens != 0 {
		t.Fatal("failed turn saved a successful continuation or changed user content")
	}
	detail, err := (&AgentSvc{}).ConversationDetail(ctx, saved.ConversationID)
	if err != nil || detail.TurnCount != 1 || detail.Usage.TotalTokens != 35 {
		t.Fatalf("failed preparation double counted: %v", err)
	}
	page, err := (&AgentSvc{}).ConversationTurns(ctx, saved.ConversationID, &dtochat.TurnPageReq{Limit: 5})
	if err != nil || len(page.Items) != 2 || page.Items[1].PreparationUsage == nil {
		t.Fatalf("history audit missing: %v", err)
	}
	messages, version, err := loadConversation(ctx, saved.ConversationID)
	if err != nil || version != 1 || len(messages) != 3 || messages[2].Role != fantasy.MessageRoleUser {
		t.Fatalf("raw failed question lost: %v", err)
	}
	if part, ok := messages[2].Content[0].(fantasy.TextPart); !ok || part.Text != "inspect missing file" {
		t.Fatal("failed user content was expanded or lost")
	}

}

func TestCompactionPartialFailureRetainsEarlierUsage(t *testing.T) {
	ctx, _ := setupChatTest(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 2 {
			http.Error(w, "fixture failure", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"s","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"Short summary."},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":12,"total_tokens":112}}`)
	}))
	defer server.Close()
	provider, err := openai.New(openai.WithBaseURL(server.URL), openai.WithAPIKey("fake"))
	if err != nil {
		t.Fatal(err)
	}
	model, err := provider.LanguageModel(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	c := &contextCompactor{window: 1000}
	messages := []fantasy.Message{fantasy.NewSystemMessage("system"), fantasy.NewUserMessage(strings.Repeat("old ", 1000)), testAssistantMessage("answer"), fantasy.NewUserMessage("continue")}
	if _, _, err := c.prepare(ctx, fantasy.PrepareStepFunctionOptions{Messages: messages, Model: model}); err == nil {
		t.Fatal("second fragment failure ignored")
	}
	audit := c.preparationUsage
	if calls.Load() != 2 || c.count != 0 || audit == nil || audit.SummaryCalls != 2 || audit.ReportedCalls != 1 || audit.UnreportedCalls != 1 || audit.Usage.TotalTokens != 112 || c.usage.TotalTokens != 112 {
		t.Fatalf("partial summary consumption lost: %+v", audit)
	}
}

func TestCompactionUsageCountsCacheCreation(t *testing.T) {
	ctx, client := setupChatTest(t)
	c := &contextCompactor{}
	c.recordPreparationUsage(&fantasy.AgentResult{TotalUsage: fantasy.Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5, CacheCreationTokens: 7, ReasoningTokens: 3}})
	usage := chatTurnUsage(&fantasy.AgentResult{TotalUsage: fantasy.Usage{InputTokens: 100, OutputTokens: 200, CacheReadTokens: 50, CacheCreationTokens: 70, ReasoningTokens: 30}}, c)
	want := token.NormalizedUsage{InputTokens: 110, OutputTokens: 220, TotalTokens: 462, CacheHitTokens: 55, CacheCreationTokens: 77, ReasoningTokens: 33}
	if usage != want {
		t.Fatalf("usage=%+v", usage)
	}
	saved := testCompletedTurn("cache-creation", 0)
	saved.Usage = usage
	saved.PreparationUsage = c.preparationUsage
	if err := saveCompletedTurn(ctx, saved); err != nil {
		t.Fatal(err)
	}
	row, err := client.KaguyaChatTurn.Query().Only(ctx)
	if err != nil || row.CacheCreationTokens != 77 || row.PreparationUsage == nil {
		t.Fatalf("cache write consumption lost: %v", err)
	}
	page, err := (&AgentSvc{}).ConversationTurns(ctx, saved.ConversationID, &dtochat.TurnPageReq{Limit: 5})
	if err != nil || page.Items[0].Usage.CacheCreationTokens != 77 {
		t.Fatalf("history cache write missing: %v", err)
	}
	frame := doneFrame(chatExecution{target: fingerprintExecution().target}, &chatOutcome{usage: usage})
	if frame.Usage.CacheCreationTokens != 77 || frame.Usage.TotalTokens != 462 {
		t.Fatal("done usage fields incomplete")
	}
	// 清理终态不得覆盖已经提交的成功结果。
	if err := markTurnEndWithPreparation(row.ID, row.ConversationID, kaguyachatturn.StatusFailed, time.Now(), c.preparationUsage, nil); !errors.Is(err, ErrConversationBusy) {
		t.Fatal("failed cleanup overwrote completed turn")
	}
	running, err := beginTurn(ctx, turnStart{ConversationID: saved.ConversationID, UserContent: "next", StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	saved = testCompletedTurn(saved.ConversationID, 1)
	saved.TurnID = running.ID
	saved.Usage = usage
	saved.PreparationUsage = c.preparationUsage
	if err := saveCompletedTurn(ctx, saved); err != nil {
		t.Fatal(err)
	}
	row, err = client.KaguyaChatTurn.Get(ctx, running.ID)
	if err != nil || row.CacheCreationTokens != 77 || row.PreparationUsage == nil {
		t.Fatalf("running cache write missing: %v", err)
	}
}
