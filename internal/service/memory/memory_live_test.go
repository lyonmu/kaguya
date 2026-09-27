package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryjob"
	"github.com/lyonmu/kaguya/internal/ent/kaguyamemoryrevision"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/internal/secret"
	"go.uber.org/zap"
)

// Explicit opt-in only: replay one failed job on an encrypted disposable copy.
// Never point this at the normal database; remote calls use the copy's task model.
func TestLiveMemoryReplay(t *testing.T) {
	path := os.Getenv("KAGUYA_MEMORY_REPLAY_COPY")
	if path == "" {
		t.Skip("requires an explicitly authorized encrypted database copy")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(filepath.Dir(resolved)), "kaguya-memory-audit-") {
		t.Fatal("replay requires a dedicated kaguya-memory-audit-* copy directory")
	}
	cfg := config.DatabaseConfig{Path: resolved, KeyFile: os.Getenv("KAGUYA_MEMORY_REPLAY_KEY")}
	key, err := cfg.SQLCipherKeyBytes()
	if err != nil {
		t.Fatal("cannot load replay database key")
	}
	if err := secret.Init(os.Getenv("KAGUYA_SECRET_KEY"), key); err != nil {
		t.Fatal("cannot initialize replay credentials")
	}
	t.Cleanup(secret.Reset)
	client, err := db.InitSQLite(&cfg)
	if err != nil {
		t.Fatal("cannot open encrypted replay copy")
	}
	t.Cleanup(func() { _ = client.Close() })
	oldID, oldLogger := global.Id, global.Logger
	global.Id, global.Logger = sharedTestID, zap.NewNop()
	t.Cleanup(func() { global.Id, global.Logger = oldID, oldLogger })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	svc := NewService(client, zap.NewNop())
	if os.Getenv("KAGUYA_MEMORY_REPLAY_READBACK") == "1" {
		revisions, err := client.KaguyaMemoryRevision.Query().Where(kaguyamemoryrevision.ActorEQ(kaguyamemoryrevision.ActorTaskModel)).All(ctx)
		if err != nil || len(revisions) == 0 {
			t.Fatal("no task-model wiki revisions available for readback")
		}
		for _, revision := range revisions {
			page, err := client.KaguyaMemoryPage.Get(ctx, revision.PageID)
			if err != nil {
				t.Fatal(err)
			}
			reader := NewScopedReader(svc, []string{page.ScopeKey})
			hits, err := reader.SearchMemory(ctx, page.Title, 10)
			if err != nil || !strings.Contains(hits, page.ID) {
				t.Fatal("published wiki page cannot be found by title")
			}
			detail, err := svc.ReadPageDetail(ctx, []string{page.ScopeKey}, page.ID, page.Version)
			if err != nil || len(detail.Claims) == 0 || detail.Body == "" {
				t.Fatal("published wiki page lacks content or evidence")
			}
			if _, err := reader.ReadMemory(ctx, page.ID, page.Version); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("verified %d task-model wiki revisions through scoped search and read", len(revisions))
		return
	}
	job, err := client.KaguyaMemoryJob.Query().Where(kaguyamemoryjob.KindEQ(kaguyamemoryjob.KindCompile), kaguyamemoryjob.StatusEQ(kaguyamemoryjob.StatusFailed)).First(ctx)
	if err != nil {
		t.Fatal("no failed compilation available for replay")
	}
	if err := svc.RetryJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	claimed := NewWorker(svc).claimDueJob(ctx)
	if claimed == nil {
		t.Fatal("replay was not claimed")
	}
	svc.runJob(ctx, claimed)
	finished, err := client.KaguyaMemoryJob.Get(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("replay status=%s error_code=%s", finished.Status, finished.ErrorCode)
	if finished.Status != kaguyamemoryjob.StatusSucceeded && finished.Status != kaguyamemoryjob.StatusNeedsReview {
		t.Fatalf("replay did not complete: %s/%s", finished.Status, finished.ErrorCode)
	}
}
