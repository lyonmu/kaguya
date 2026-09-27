package memory

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	dtomemory "github.com/lyonmu/kaguya/internal/dto/memory"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryjob"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemorysource"
)

func TestMemoryDetailEmptyCollectionsAndProgressiveRead(t *testing.T) {
	ctx, svc, _ := setupMemoryTest(t)
	page, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{
		ScopeKey: ScopePersonal, Kind: "fact", Title: "索引标题", Summary: "简短说明", Body: "仅在读取工具中出现的完整正文",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"related_ids":[]`) || !strings.Contains(string(raw), `"aliases":[]`) {
		t.Fatalf("empty collections must be arrays: %s", raw)
	}
	selection, err := svc.AutoRecall(ctx, RecallOptions{Scopes: []string{ScopePersonal}, Query: "索引标题", ContextTokens: 2000, Window: 32000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(selection.Text, "简短说明") || !strings.Contains(selection.Text, "memory_read") || strings.Contains(selection.Text, page.Body) {
		t.Fatalf("invalid progressive catalog: %s", selection.Text)
	}
	reader := NewScopedReader(svc, []string{ScopePersonal})
	search, err := reader.SearchMemory(ctx, "索引标题", 5)
	if err != nil || strings.Contains(search, page.Body) {
		t.Fatalf("search=%s err=%v", search, err)
	}
	full, err := reader.ReadMemory(ctx, page.ID, 0)
	if err != nil || !strings.Contains(full, page.Body) {
		t.Fatalf("read=%s err=%v", full, err)
	}
}

func TestMemoryMetadataAndStatusPreserveProvenance(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	caller.steps = []func(int) (CallResult, error){
		func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
		func(int) (CallResult, error) { return okResult(planJSON(testCreateChange(f.sourceID))), nil },
	}
	f.run(t)
	row, err := f.client.KaguyaMemoryPage.Query().Only(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.svc.ManageDetail(f.ctx, row.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	flag := true
	expires := nowTime().Add(time.Hour)
	after, err := f.svc.UpdatePage(f.ctx, row.ID, &dtomemory.MemoryPageUpdateReq{
		ExpectedVersion: before.Version, Pinned: &flag, UserLocked: &flag, ExpiresAt: &expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Claims, after.Claims) || after.SourceCount != 1 || after.ExpiresAt == nil || !after.ExpiresAt.Equal(expires) {
		t.Fatalf("metadata edit changed provenance or lost expiry: %+v", after)
	}
	if err := f.svc.DeletePage(f.ctx, row.ID, "disable"); err != nil {
		t.Fatal(err)
	}
	disabled, err := f.svc.ManageDetail(f.ctx, row.ID, 0)
	if err != nil || !reflect.DeepEqual(before.Claims, disabled.Claims) {
		t.Fatalf("disable lost evidence: %+v %v", disabled, err)
	}
	shared, err := f.svc.UpdatePage(f.ctx, row.ID, &dtomemory.MemoryPageUpdateReq{ExpectedVersion: disabled.Version, ScopeKey: ScopeShared})
	if err != nil || shared.ScopeKey != ScopeShared {
		t.Fatalf("scope not saved: %+v %v", shared, err)
	}
}

func TestOversizedSourceBecomesVisibleBlockedJob(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	sourceID := f.addTurn(t, strings.Repeat("长", 3000))
	source, err := f.client.KaguyaMemorySource.Get(f.ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.svc.createJobForSources(f.ctx, []*ent.KaguyaMemorySource{source}, minSourceBatch)
	if err != nil || job == nil {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if job.Status != kaguyamemoryjob.StatusBlocked || job.ErrorCode != "input_budget" {
		t.Fatalf("job=%+v", job)
	}
	source, err = f.client.KaguyaMemorySource.Get(f.ctx, sourceID)
	if err != nil || source.State != kaguyamemorysource.StateClaimed {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	input, err := f.svc.loadJobInput(f.ctx, job)
	if err != nil || !strings.Contains(string(input), strings.Repeat("长", 3000)) {
		t.Fatalf("frozen source lost: %v", err)
	}
}

func TestClaimKeysArePageLocal(t *testing.T) {
	a, b := validChange(), validChange()
	b.CanonicalKey = "another-topic"
	plan := &dtomemory.PatchPlan{SchemaVersion: 1, Changes: []dtomemory.PagePatch{a, b}}
	if err := ValidatePlan(validationInput(plan, testProjection())); err != nil {
		t.Fatal(err)
	}
	a.Claims = append(a.Claims, a.Claims[0])
	plan.Changes = []dtomemory.PagePatch{a}
	if err := ValidatePlan(validationInput(plan, testProjection())); err == nil {
		t.Fatal("duplicate claims in one page must be rejected")
	}
}

func TestNoteSourcesRemainImmutableAndAreForgotten(t *testing.T) {
	ctx, svc, client := setupMemoryTest(t)
	page, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{ScopeKey: ScopePersonal, Kind: "fact", Title: "笔记", Body: strings.Repeat("旧内容", 100)})
	if err != nil {
		t.Fatal(err)
	}
	oldSource := page.Claims[0].Evidence[0].SourceID
	body := "新的事实"
	updated, err := svc.UpdatePage(ctx, page.ID, &dtomemory.MemoryPageUpdateReq{ExpectedVersion: page.Version, Body: &body})
	if err != nil {
		t.Fatal(err)
	}
	newSource := updated.Claims[0].Evidence[0].SourceID
	if oldSource == newSource {
		t.Fatal("editing must create a new immutable source")
	}
	src, err := client.KaguyaMemorySource.Get(ctx, oldSource)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSourceQuote(ctx, client, src, page.Claims[0].Evidence[0].PartKey, page.Claims[0].Evidence[0].Quote); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RestoreRevision(ctx, []string{ScopeShared}, page.ID, 1); err != ErrPageForbidden {
		t.Fatalf("cross-scope restore: %v", err)
	}
	if err := svc.DeletePage(ctx, page.ID, "forget"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{oldSource, newSource} {
		if _, err := svc.SourceDetail(ctx, id); err != ErrSourceNotFound {
			t.Fatalf("forgotten source accessible: %v", err)
		}
	}
}

func TestLockedMemoryReviewPublishesAndChecksSources(t *testing.T) {
	for _, excluded := range []bool{false, true} {
		t.Run(map[bool]string{false: "approve", true: "excluded"}[excluded], func(t *testing.T) {
			caller := &fakeCaller{}
			f := setupCompile(t, caller)
			locked := true
			existing, err := f.svc.CreatePage(f.ctx, &dtomemory.MemoryPageSaveReq{ScopeKey: f.scope, Kind: "decision", CanonicalKey: "memory-storage", Title: "锁定的知识", Body: "原始正文", UserLocked: &locked})
			if err != nil {
				t.Fatal(err)
			}
			change := testCreateChange(f.sourceID)
			change.Action, change.PageID, change.BaseVersion = "update", existing.ID, existing.Version
			change.Claims[0].Basis = "synthesis"
			caller.steps = []func(int) (CallResult, error){
				func(int) (CallResult, error) { return okResult(extractJSON(testCandidate(f.sourceID))), nil },
				func(int) (CallResult, error) { return okResult(planJSON(change)), nil },
			}
			f.run(t)
			job, err := f.client.KaguyaMemoryJob.Query().Only(f.ctx)
			if err != nil || job.Status != kaguyamemoryjob.StatusNeedsReview {
				t.Fatalf("missing review: %+v %v", job, err)
			}
			if count, _ := f.client.KaguyaMemoryPage.Query().Count(f.ctx); count != 1 {
				t.Fatal("locked page replaced before approval")
			}
			if excluded {
				if err := f.client.KaguyaMemorySource.UpdateOneID(f.sourceID).SetState(kaguyamemorysource.StateExcluded).Exec(f.ctx); err != nil {
					t.Fatal(err)
				}
			}
			result, err := f.svc.ApproveReview(f.ctx, job)
			if excluded {
				if err == nil {
					t.Fatal("approval revived excluded source")
				}
				return
			}
			if err != nil || result.Published != 1 {
				t.Fatalf("approval=%+v %v", result, err)
			}
			page, err := f.client.KaguyaMemoryPage.Query().Only(f.ctx)
			if err != nil || string(page.Status) != "active" {
				t.Fatalf("page=%+v %v", page, err)
			}
			detail, err := f.svc.ManageDetail(f.ctx, page.ID, 0)
			if err != nil || detail.Claims[0].Basis != "synthesis" {
				t.Fatal("approval must retain original evidence basis")
			}
		})
	}
}

func TestAutomaticOrganizingPauseAndQueueFairness(t *testing.T) {
	caller := &fakeCaller{}
	f := setupCompile(t, caller)
	backdateSources(t, f.ctx, f.client, 2*time.Minute)
	setupPolicy(t, f.ctx, f.client, true, false)
	f.worker.processOnce(f.ctx)
	if count, _ := f.client.KaguyaMemoryJob.Query().Count(f.ctx); count != 0 {
		t.Fatal("automatic organizing continued while paused")
	}
	setupPolicy(t, f.ctx, f.client, true, true)
	off := makeConversation(t, f.ctx, f.client, "disabled-backlog", "", kaguyaconversation.MemoryModeOff)
	rows := make([]*ent.KaguyaMemorySourceCreate, 0, 256)
	for i := 0; i < 256; i++ {
		rows = append(rows, f.client.KaguyaMemorySource.Create().SetSourceKey(fmt.Sprintf("disabled-%d", i)).SetKind(kaguyamemorysource.KindTurn).
			SetScopeKey(ScopePersonal).SetConversationID(off).SetCapturedAt(nowTime().Add(-time.Hour)).SetState(kaguyamemorysource.StatePending))
	}
	if err := f.client.KaguyaMemorySource.CreateBulk(rows...).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	job, _ := f.worker.claimReadyBatch(f.ctx, "")
	if job == nil || job.ConversationID != f.conv {
		t.Fatalf("disabled backlog starved active source: %+v", job)
	}
}

func TestExpiredSearchHitsDoNotHideCurrentMemory(t *testing.T) {
	ctx, svc, _ := setupMemoryTest(t)
	expired := nowTime().Add(-time.Hour)
	for i := 0; i < ftsCandidateLimit; i++ {
		_, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{ScopeKey: ScopePersonal, Kind: "fact", CanonicalKey: fmt.Sprintf("expired-%d", i), Title: "数据库", Body: "数据库", ExpiresAt: &expired})
		if err != nil {
			t.Fatal(err)
		}
	}
	current, err := svc.CreatePage(ctx, &dtomemory.MemoryPageSaveReq{ScopeKey: ScopePersonal, Kind: "fact", CanonicalKey: "current", Title: "数据库", Body: "数据库 当前仍然有效的记录"})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := svc.SearchPages(ctx, []string{ScopePersonal}, "数据库", 5, false)
	if err != nil || len(hits) != 1 || hits[0].ID != current.ID {
		t.Fatalf("expired hits hid current page: %+v %v", hits, err)
	}
}
