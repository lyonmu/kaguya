package system

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
)

func TestModelCatalogSyncAndQuery(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["User-Agent"]; ok {
			t.Errorf("unexpected User-Agent=%q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"openai/gpt-test":{"id":"openai/gpt-test","name":"GPT Test","family":"gpt","description":"OpenAI test model","reasoning":true,"tool_call":true,"structured_output":true,"release_date":"2026-08-15","last_updated":"2026-09-01","modalities":{"input":["text","image"]},"limit":{"context":128000,"output":32000}},
			"anthropic/claude-test":{"id":"anthropic/claude-test","name":"Claude Test","family":"claude","description":"Newest Anthropic model","reasoning":false,"tool_call":true,"release_date":"2026-09-10","last_updated":"2026-09-11","modalities":{"input":["text"]},"limit":{"context":200000,"output":8000}}
		}`)
	}))
	defer server.Close()

	if err := db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetModelSyncURL(server.URL).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	syncer := NewModelCatalogSyncer(server.Client())
	result, err := syncer.Sync(ctx)
	if err != nil || result.Count != 2 || result.SyncedAt.IsZero() {
		t.Fatalf("sync result=%+v err=%v", result, err)
	}
	info, err := (&SystemSvc{}).Info(ctx)
	if err != nil || info.ModelSyncCatalogCount != 2 || info.ModelSyncLastSuccessAt == nil || info.ModelSyncLastError != "" {
		t.Fatalf("system info=%+v err=%v", info, err)
	}
	latest, err := (&SystemSvc{}).ModelCatalog(ctx, &dtosystem.SystemModelCatalogReq{Page: 1, PageSize: 10})
	if err != nil || len(latest.Items) != 2 || latest.Items[0].ID != "anthropic/claude-test" {
		t.Fatalf("latest-first catalog=%+v err=%v", latest, err)
	}
	page, err := (&SystemSvc{}).ModelCatalog(ctx, &dtosystem.SystemModelCatalogReq{Keyword: "gpt", Page: 1, PageSize: 10})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("catalog=%+v err=%v", page, err)
	}
	item := page.Items[0]
	if item.ID != "openai/gpt-test" || item.Lab != "openai" || item.Family != "gpt" || item.Description != "OpenAI test model" || item.ReleaseDate != "2026-08-15" || item.ReasoningEnabled != consts.IsTrue || item.CapabilityToolUse != consts.IsTrue || item.CapabilityVision != consts.IsTrue || item.CapabilityStructuredOutput != consts.IsTrue || item.TokenContextWindow != 128000 || item.TokenMaxOutputTokens != 32000 {
		t.Fatalf("mapped catalog item=%+v", item)
	}
	byLab, err := (&SystemSvc{}).ModelCatalog(ctx, &dtosystem.SystemModelCatalogReq{Keyword: "anthropic", Page: 1, PageSize: 10})
	if err != nil || byLab.Total != 1 || byLab.Items[0].ID != "anthropic/claude-test" {
		t.Fatalf("catalog search by lab=%+v err=%v", byLab, err)
	}
}

func TestModelCatalogSyncFailurePreservesCatalog(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	if err := db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetModelCatalogJSON(`[{"id":"saved","name":"Saved"}]`).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	if err := db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetModelSyncURL(server.URL).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := NewModelCatalogSyncer(server.Client()).Sync(context.Background()); err == nil {
		t.Fatal("expected sync failure")
	}
	row, err := db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil || row.ModelCatalogJSON != `[{"id":"saved","name":"Saved"}]` || row.ModelSyncLastAttemptAt == nil || row.ModelSyncLastError == "" {
		t.Fatalf("row=%+v err=%v", row, err)
	}
}
