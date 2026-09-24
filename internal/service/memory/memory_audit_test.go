package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryjob"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
)

func TestCompilePlanReceivesFrozenOldPage(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	page, err := f.svc.CreatePage(f.ctx, &dtomemory.MemoryPageSaveReq{ScopeKey: f.scope, Kind: "decision", CanonicalKey: "memory-storage", Title: "Memory 存储", Body: "必须保留的旧约束 api_key=sk-abcdefgh12345678"})
	if err != nil {
		t.Fatal(err)
	}
	change := testCreateChange(f.sourceID)
	change.Action, change.PageID, change.BaseVersion = "update", page.ID, page.Version
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
		func(i int) (CallResult, error) {
			for _, want := range []string{page.ID, "必须保留的旧约束", `"version":1`, `"evidence":`} {
				if !strings.Contains(caller.prompts[i], want) {
					t.Errorf("missing %q in plan input", want)
				}
			}
			if strings.Contains(caller.prompts[i], "sk-abcdefgh12345678") {
				t.Error("old page secret leaked")
			}
			return okResult(planJSON(change)), nil
		},
	}
	f.run(t)
}

func TestNoopCannotCompleteStolenLease(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	backdateSources(t, f.ctx, f.client, 2*time.Minute)
	job, _ := f.worker.claimReadyBatch(f.ctx, "")
	if job == nil {
		t.Fatal("missing job")
	}
	input, err := f.svc.loadJobInput(f.ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.client.KaguyaMemoryJob.UpdateOneID(job.ID).SetLeaseToken("new-worker").Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.svc.noopJob(f.ctx, job, projectionsFromPayload(input))
	current, err := f.client.KaguyaMemoryJob.Get(f.ctx, job.ID)
	if err != nil || current.Status != kaguyamemoryjob.StatusRunning || current.LeaseToken != "new-worker" {
		t.Fatalf("job=%+v err=%v", current, err)
	}
	src, err := f.client.KaguyaMemorySource.Get(f.ctx, f.sourceID)
	if err != nil || src.State != kaguyamemorysource.StateClaimed || src.CursorPart != 0 {
		t.Fatalf("source=%+v err=%v", src, err)
	}
}

func TestEvidenceCannotUpgradeAssistantAssertion(t *testing.T) {
	change := validChange()
	projections := testProjection()
	projections[0].Segments[0].Origin = OriginAssistantAssertion
	plan := &dtomemory.PatchPlan{SchemaVersion: 1, Changes: []dtomemory.PagePatch{change}}
	if err := ValidatePlan(validationInput(plan, projections)); err == nil {
		t.Fatal("assistant assertion upgraded to user statement")
	}
	plan.Changes[0].Claims[0].Basis = "synthesis"
	if err := ValidatePlan(validationInput(plan, projections)); err != nil {
		t.Fatal(err)
	}
	if strongEvidence(&plan.Changes[0]) {
		t.Fatal("synthesis must not activate page")
	}
}

func TestAllClaimsNeedSupportingStrongEvidence(t *testing.T) {
	change := validChange()
	weak := change.Claims[0]
	weak.Key, weak.Basis = "inference", "synthesis"
	change.Claims = append(change.Claims, weak)
	if strongEvidence(&change) {
		t.Fatal("one strong claim must not activate other inferences")
	}
	change = validChange()
	change.Claims[0].Evidence[0].Relation = "refute"
	if strongEvidence(&change) {
		t.Fatal("refuting evidence must not count as support")
	}
}

func TestTransientRecallRespectsBudgetAndExpiry(t *testing.T) {
	f := setupCompile(t, &fakeCaller{})
	page, err := f.svc.CreatePage(f.ctx, &dtomemory.MemoryPageSaveReq{ScopeKey: f.scope, Kind: "fact", Title: "SQLCipher", Body: "内容", Summary: strings.Repeat("预算", 100)})
	if err != nil {
		t.Fatal(err)
	}
	refs := []dtomemory.TurnMemoryRef{{PageID: page.ID, Version: page.Version}}
	text, _, err := f.svc.RenderTransient(f.ctx, refs, 20)
	if err != nil || estimateTextTokens(text) > 20 {
		t.Fatalf("tokens=%d err=%v", estimateTextTokens(text), err)
	}
	if err := f.client.KaguyaMemoryPage.UpdateOneID(page.ID).SetExpiresAt(time.Now().Add(-time.Second)).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	text, kept, err := f.svc.RenderTransient(f.ctx, refs)
	if err != nil || text != "" || len(kept) != 0 {
		t.Fatalf("expired recall retained: refs=%v err=%v", kept, err)
	}
}

func TestLongSourceDoesNotLoseTail(t *testing.T) {
	text := strings.Repeat("字", maxSegmentChars*65) + "尾部证据"
	segments := splitSegments("user", OriginUserStatement, text)
	var rebuilt strings.Builder
	for _, segment := range segments {
		rebuilt.WriteString(segment.Text)
	}
	if rebuilt.String() != text {
		t.Fatal("source tail silently discarded")
	}
}

func TestCanceledCallStillRecordsKnownUsage(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	backdateSources(t, f.ctx, f.client, 2*time.Minute)
	job, _ := f.worker.claimReadyBatch(f.ctx, "")
	if job == nil {
		t.Fatal("missing job")
	}
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	caller.steps = []func(int) (CallResult, error){func(int) (CallResult, error) { cancel(); return okResult(extractJSON()), context.Canceled }}
	_, _ = f.svc.Extract(ctx, job, fakeTaskModel(), []byte("[]"))
	attempts, err := f.client.KaguyaMemoryAttempt.Query().All(f.ctx)
	if err != nil || len(attempts) != 1 || !attempts[0].UsageKnown || attempts[0].TotalTokens != 15 || attempts[0].ResultCode != "canceled" {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
}

func TestDisabledMemoryDoesNotSendPendingSources(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	setupPolicy(t, f.ctx, f.client, false, false)
	backdateSources(t, f.ctx, f.client, 2*time.Minute)
	if job, _ := f.worker.claimReadyBatch(f.ctx, ""); job != nil {
		t.Fatal("disabled memory claimed sources")
	}
	if caller.mu.Load() != 0 {
		t.Fatal("disabled memory called model")
	}
}

func TestPolicyRevokedBetweenCompilerStages(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	caller.steps = []func(int) (CallResult, error){func(int) (CallResult, error) {
		if err := BumpPolicyEpoch(f.ctx, f.client); err != nil {
			t.Fatal(err)
		}
		return okResult(extractJSON(testCandidate(f.sourceID))), nil
	}}
	job := f.run(t)
	current, err := f.client.KaguyaMemoryJob.Get(f.ctx, job.ID)
	if err != nil || current.Status != kaguyamemoryjob.StatusCanceled {
		t.Fatalf("job=%+v err=%v", current, err)
	}
	if caller.mu.Load() != 1 {
		t.Fatal("revoked input sent to planning stage")
	}
}

func TestConversationReaderRevokedMidTurn(t *testing.T) {
	f := setupCompile(t, &fakeCaller{})
	policy, err := LoadPolicy(f.ctx, f.client)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewConversationReader(f.svc, []string{f.scope}, f.conv, policy.Epoch)
	if _, err := reader.SearchMemory(f.ctx, "SQLCipher", 5); err != nil {
		t.Fatal(err)
	}
	if err := BumpPolicyEpoch(f.ctx, f.client); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.SearchMemory(f.ctx, "SQLCipher", 5); err == nil {
		t.Fatal("revoked search allowed")
	}
	if _, err := reader.ReadMemory(f.ctx, "any-page", 0); err == nil || err.Error() != "memory policy changed" {
		t.Fatalf("expected policy rejection, got %v", err)
	}
}

func TestPlanRepairUsesPlanContract(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
		func(int) (CallResult, error) { return okResult("not JSON"), nil },
		func(i int) (CallResult, error) {
			if !strings.HasPrefix(caller.prompts[i], planSystemPrompt) {
				t.Error("plan repaired using extraction contract")
			}
			return okResult(planJSON(testCreateChange(f.sourceID))), nil
		},
	}
	f.run(t)
	count, err := f.client.KaguyaMemoryPage.Query().Count(f.ctx)
	if err != nil || count != 1 {
		t.Fatalf("pages=%d err=%v", count, err)
	}
}

func TestProviderErrorDoesNotPersistResponseText(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	backdateSources(t, f.ctx, f.client, 2*time.Minute)
	job, _ := f.worker.claimReadyBatch(f.ctx, "")
	if job == nil {
		t.Fatal("missing job")
	}
	if err := f.svc.failJob(f.ctx, job, "provider_error", "private echoed request"); err != nil {
		t.Fatal(err)
	}
	current, err := f.client.KaguyaMemoryJob.Get(f.ctx, job.ID)
	if err != nil || strings.Contains(current.ErrorSummary, "private") {
		t.Fatalf("job=%+v err=%v", current, err)
	}
}
