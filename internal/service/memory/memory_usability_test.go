package memory

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

	agentruntime "github.com/lyonmu/kaguya/internal/agent/runtime"
	"github.com/lyonmu/kaguya/internal/consts"
	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryjob"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorypage"
)

func TestMemoryRuntimeStreamsAndUsesModelOutputLimit(t *testing.T) {
	for _, reason := range []string{"stop", "length"} {
		t.Run(reason, func(t *testing.T) {
			_, _, _ = setupMemoryTest(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req["stream"] != true {
					t.Error("memory must stream so reasoning does not wait for a complete HTTP response")
				}
				if req["max_completion_tokens"] != float64(384000) && req["max_tokens"] != float64(384000) {
					t.Error("task model output capability was overridden")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"{}\"}}]}\n\n")
				fmt.Fprintf(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":6000,\"total_tokens\":6010,\"completion_tokens_details\":{\"reasoning_tokens\":5900}}}\n\ndata: [DONE]\n\n", reason)
			}))
			defer server.Close()
			target := fakeTaskModel()
			target.TokenMaxOutputTokens, target.TokenContextWindow = 384000, 1000000
			result, err := (RuntimeCaller{}).Call(context.Background(), agentruntime.ProviderConfig{
				Protocol: consts.ProtocolOpenAIChat, BaseURL: server.URL, RequestPath: "/v1/chat/completions", ModelID: "test", APIKey: "test",
			}, "memory", "[]", callOutputLimit(target, 1000))
			if reason == "length" {
				if !errors.Is(err, errOutputLimit) || classifyCode(err) != "output_limit" || callRetryable(err) {
					t.Fatalf("truncation misclassified: %v", err)
				}
			} else if err != nil || result.Text != "{}" {
				t.Fatalf("stream result=%s err=%v", result.Text, err)
			}
			if !result.UsageKnown || result.Usage.ReasoningTokens != 5900 {
				t.Fatalf("lost usage: %+v", result.Usage)
			}
		})
	}
}

func TestLargeWikiPageIsStoredAndReadWithoutLoss(t *testing.T) {
	ctx, svc, _ := setupMemoryTest(t)
	body := strings.Repeat("完整知识与上下文。", 3000)
	page, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{ScopeKey: ScopePersonal, Kind: "fact", Title: strings.Repeat("中文长标题", 40), Body: body})
	if err != nil {
		t.Fatal(err)
	}
	reader := NewScopedReader(svc, []string{ScopePersonal})
	catalog, err := reader.SearchMemory(ctx, "", 5)
	if err != nil || !strings.Contains(catalog, page.ID) || strings.Contains(catalog, body) {
		t.Fatalf("catalog unavailable: %v", err)
	}
	var restored strings.Builder
	offset := 0
	for {
		raw, err := reader.ReadMemory(ctx, page.ID, page.Version, offset, 8000)
		if err != nil {
			t.Fatal(err)
		}
		var part struct {
			Body string `json:"body"`
			Next *int   `json:"next_offset"`
		}
		if err := json.Unmarshal([]byte(raw), &part); err != nil {
			t.Fatal(err)
		}
		restored.WriteString(part.Body)
		if part.Next == nil {
			break
		}
		if *part.Next <= offset {
			t.Fatal("read made no progress")
		}
		offset = *part.Next
	}
	if restored.String() != body {
		t.Fatal("long wiki content was truncated")
	}
	change := validChange()
	change.Body = body
	change.Summary = strings.Repeat("摘要", 200)
	if err := ValidatePlan(validationInput(&dtomemory.PatchPlan{SchemaVersion: 1, Changes: []dtomemory.PagePatch{change}}, testProjection())); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationPersistsRetrySchedule(t *testing.T) {
	f := setupCompile(t, &fakeCaller{})
	backdateSources(t, f.ctx, f.client, 2*time.Minute)
	job, _ := f.worker.claimReadyBatch(f.ctx, "")
	if job == nil {
		t.Fatal("missing job")
	}
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	f.svc.retryLater(ctx, job, time.Second, "canceled")
	got, err := f.client.KaguyaMemoryJob.Get(f.ctx, job.ID)
	if err != nil || got.Status != kaguyamemoryjob.StatusRetryWait {
		t.Fatalf("retry lost on cancellation: %v", err)
	}
}

func TestModelCanPublishSynthesisWithoutManualReview(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	change := testCreateChange(f.sourceID)
	change.Claims[0].Basis = "synthesis"
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
		func(int) (CallResult, error) { return okResult(planJSON(change)), nil },
	}
	job := f.run(t)
	finished, err := f.client.KaguyaMemoryJob.Get(f.ctx, job.ID)
	if err != nil || finished.Status != kaguyamemoryjob.StatusSucceeded {
		t.Fatalf("model decision was blocked: %v", err)
	}
	page, err := f.client.KaguyaMemoryPage.Query().Only(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.svc.ReadPageDetail(f.ctx, []string{f.scope}, page.ID, 0)
	if err != nil || detail.Claims[0].Basis != "synthesis" {
		t.Fatal("inference was promoted to confirmed evidence")
	}
	hits, err := f.svc.SearchPages(f.ctx, []string{f.scope}, "SQLCipher", 5, false)
	if err != nil || len(hits) != 1 {
		t.Fatal("published knowledge cannot be recalled")
	}
}

func TestCompilerReceivesExistingWikiLinks(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	linked, err := f.svc.CreatePage(f.ctx, &dtomemory.MemoryPageSaveReq{ScopeKey: f.scope, Kind: "fact", Title: "部署约定", Body: "保存应用数据。"})
	if err != nil {
		t.Fatal(err)
	}
	existing, err := f.svc.CreatePage(f.ctx, &dtomemory.MemoryPageSaveReq{ScopeKey: f.scope, Kind: "decision", CanonicalKey: "memory-storage", Title: "存储", Body: "旧正文", RelatedIDs: []string{linked.ID}})
	if err != nil {
		t.Fatal(err)
	}
	change := testCreateChange(f.sourceID)
	change.Action, change.PageID, change.BaseVersion = "update", existing.ID, existing.Version
	change.RelatedIDs = []string{linked.ID}
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
		func(i int) (CallResult, error) {
			if !strings.Contains(caller.prompts[i], `"related_ids":["`+linked.ID+`"]`) {
				t.Error("old links missing from wiki compiler input")
			}
			return okResult(planJSON(change)), nil
		},
	}
	f.run(t)
	detail, err := f.svc.ReadPageDetail(f.ctx, []string{f.scope}, existing.ID, 0)
	if err != nil || detail.Version != 2 || len(detail.RelatedIDs) != 1 || detail.RelatedIDs[0] != linked.ID {
		t.Fatalf("wiki links were lost: %v", err)
	}
}

func TestAutomaticUpdateDoesNotReactivateArchivedPage(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	page, err := f.svc.CreatePage(f.ctx, &dtomemory.MemoryPageSaveReq{ScopeKey: f.scope, Kind: "decision", CanonicalKey: "memory-storage", Title: "存储", Summary: "存储约定", Body: "旧正文"})
	if err != nil {
		t.Fatal(err)
	}
	change := testCreateChange(f.sourceID)
	change.Action, change.PageID, change.BaseVersion = "update", page.ID, page.Version
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
		func(int) (CallResult, error) {
			// The user disables the page while the model is preparing an update.
			if err := f.client.KaguyaMemoryPage.UpdateOneID(page.ID).SetStatus(kaguyamemorypage.StatusArchived).Exec(f.ctx); err != nil {
				t.Fatal(err)
			}
			return okResult(planJSON(change)), nil
		},
	}
	f.run(t)
	got, err := f.client.KaguyaMemoryPage.Get(f.ctx, page.ID)
	if err != nil || got.Status != kaguyamemorypage.StatusArchived || got.Body != "旧正文" {
		t.Fatalf("automatic update reactivated user-disabled knowledge: %v", err)
	}
}

func TestLongTopicKeysDoNotCollideOnSharedPrefix(t *testing.T) {
	prefix := strings.Repeat("项目知识", 50)
	a, b := deriveCanonicalKey(prefix+"甲"), deriveCanonicalKey(prefix+"乙")
	if a == b || len(a) > maxCanonicalBytes || len(b) > maxCanonicalBytes {
		t.Fatal("long topic titles must retain distinct valid database keys")
	}
}

func TestNaturalChineseQuestionsRecallShortTopics(t *testing.T) {
	ctx, svc, _ := setupMemoryTest(t)
	page, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: ScopePersonal, Kind: "decision", Title: "加密", Summary: "采用 SQLCipher", Body: "存储使用 SQLCipher。",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"怎么加密", "请帮我回顾一下之前讨论过的加密方案", "关于这个项目我还有一个问题想了解 SQLCipher"} {
		hits, err := svc.SearchPages(ctx, []string{ScopePersonal}, query, 5, false)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, hit := range hits {
			found = found || hit.ID == page.ID
		}
		if !found {
			t.Errorf("question %q missed its topic", query)
		}
	}
}

func TestManualRetryRunsWithAutomaticLearningPaused(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	backdateSources(t, f.ctx, f.client, 2*time.Minute)
	job, _ := f.worker.claimReadyBatch(f.ctx, "")
	if job == nil {
		t.Fatal("missing job")
	}
	if err := f.svc.failJob(f.ctx, job, "invalid_output", "invalid output"); err != nil {
		t.Fatal(err)
	}
	setupPolicy(t, f.ctx, f.client, true, false)
	if err := f.svc.RetryJob(f.ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	caller.steps = []func(int) (CallResult, error){func(int) (CallResult, error) { return okResult(extractJSON()), nil }}
	f.worker.processOnce(f.ctx)
	got, err := f.client.KaguyaMemoryJob.Get(f.ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != kaguyamemoryjob.StatusSucceeded {
		t.Fatalf("explicit retry stalled: %s", got.Status)
	}
}

func TestCompileRepairsInvalidEvidenceWithOriginalSources(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	bad := testCandidate(f.sourceID)
	bad.Evidence[0].Quote = "不是原文的摘录"
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(bad)), nil },
		func(i int) (CallResult, error) {
			if !strings.Contains(caller.prompts[i], "这个项目继续用 SQLCipher，不引入第二个数据库。") {
				t.Error("repair lacks the original source needed to fix evidence")
			}
			return okResult(extractJSON(testCandidate(f.sourceID))), nil
		},
		func(int) (CallResult, error) { return okResult(planJSON(testCreateChange(f.sourceID))), nil },
	}
	job := f.run(t)
	got, err := f.client.KaguyaMemoryJob.Get(f.ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != kaguyamemoryjob.StatusSucceeded {
		t.Fatalf("repairable response failed: %s/%s", got.Status, got.ErrorCode)
	}
}

func TestMissingResultArrayIsNotSuccessfulNoop(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(`{"schema_version":1}`), nil },
		func(int) (CallResult, error) { return okResult(`{"schema_version":1}`), nil },
	}
	job := f.run(t)
	got, err := f.client.KaguyaMemoryJob.Get(f.ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != kaguyamemoryjob.StatusFailed || got.ErrorCode != "invalid_output" {
		t.Fatalf("missing candidates silently consumed source: %s/%s", got.Status, got.ErrorCode)
	}
}
