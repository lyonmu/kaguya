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

// 提供商目录来自 api.json，模型目录来自 models.json；两个地址分别配置、分别解析。
func TestModelCatalogSyncAndQuery(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	providerCatalog := `{
		"openai":{"id":"openai","name":"OpenAI","api":"https://api.openai.com/v1","npm":"@ai-sdk/openai","doc":"https://platform.openai.com/docs","models":{
			"gpt-test":{"id":"gpt-test","name":"GPT Test"}}},
		"anthropic":{"id":"anthropic","name":"Anthropic","api":"https://api.anthropic.com/v1","npm":"@ai-sdk/anthropic","models":{
			"claude-test":{"id":"claude-test","name":"Claude Test"}}},
		"nvidia":{"id":"nvidia","name":"NVIDIA","npm":"@ai-sdk/openai-compatible","models":{
			"nvidia-test":{"id":"nvidia-test","name":"NVIDIA Test"}}}
	}`
	modelCatalog := `{
		"openai/gpt-test":{"id":"openai/gpt-test","name":"GPT Test","family":"gpt","description":"OpenAI test model","reasoning":true,"tool_call":true,"structured_output":true,"release_date":"2026-08-15","last_updated":"2026-09-01","modalities":{"input":["text","image"]},"limit":{"context":128000,"output":32000}},
		"anthropic/claude-test":{"id":"anthropic/claude-test","name":"Claude Test","family":"claude","description":"Newest Anthropic model","reasoning":false,"tool_call":true,"release_date":"2026-09-10","last_updated":"2026-09-11","modalities":{"input":["text"]},"limit":{"context":200000,"output":8000}},
		"nvidia/nvidia-test":{"id":"nvidia/nvidia-test","name":"NVIDIA Test","release_date":"2026-01-01","limit":{"context":1000,"output":100}}
	}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["User-Agent"]; ok {
			t.Errorf("unexpected User-Agent=%q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api.json":
			fmt.Fprint(w, providerCatalog)
		case "/models.json":
			fmt.Fprint(w, modelCatalog)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).
		SetProviderSyncURL(server.URL + "/api.json").SetModelSyncURL(server.URL + "/models.json").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	syncer := NewModelCatalogSyncer(server.Client())
	result, err := syncer.Sync(ctx)
	if err != nil || result.Count != 3 || result.ProviderCount != 2 || result.SyncedAt.IsZero() {
		t.Fatalf("sync result=%+v err=%v", result, err)
	}
	info, err := (&SystemSvc{}).Info(ctx)
	if err != nil || info.ModelSyncCatalogCount != 3 || info.ProviderCatalogCount != 2 || info.ModelSyncLastSuccessAt == nil || info.ModelSyncLastError != "" {
		t.Fatalf("system info=%+v err=%v", info, err)
	}
	// 模型目录保持全量：没有 api 的 nvidia 也要出现在模型目录里。
	latest, err := (&SystemSvc{}).ModelCatalog(ctx, &dtosystem.SystemModelCatalogReq{Page: 1, PageSize: 10})
	if err != nil || len(latest.Items) != 3 || latest.Items[0].ID != "anthropic/claude-test" {
		t.Fatalf("latest-first catalog=%+v err=%v", latest, err)
	}
	page, err := (&SystemSvc{}).ModelCatalog(ctx, &dtosystem.SystemModelCatalogReq{Keyword: "gpt", Page: 1, PageSize: 10})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("catalog=%+v err=%v", page, err)
	}
	item := page.Items[0]
	if item.ID != "openai/gpt-test" || item.ProviderID != "openai" || item.ProviderName != "OpenAI" || item.ModelID != "gpt-test" || item.Family != "gpt" || item.Description != "OpenAI test model" || item.ReleaseDate != "2026-08-15" || item.ReasoningEnabled != consts.IsTrue || item.CapabilityToolUse != consts.IsTrue || item.CapabilityVision != consts.IsTrue || item.CapabilityStructuredOutput != consts.IsTrue || item.TokenContextWindow != 128000 || item.TokenMaxOutputTokens != 32000 {
		t.Fatalf("mapped catalog item=%+v", item)
	}
	byProvider, err := (&SystemSvc{}).ModelCatalog(ctx, &dtosystem.SystemModelCatalogReq{Keyword: "anthropic", Page: 1, PageSize: 10})
	if err != nil || byProvider.Total != 1 || byProvider.Items[0].ID != "anthropic/claude-test" || byProvider.Items[0].ProviderName != "Anthropic" {
		t.Fatalf("catalog search by provider=%+v err=%v", byProvider, err)
	}

	// 提供商目录只收录有 api 的提供商，并按名称 A→Z。
	providers, err := (&SystemSvc{}).ProviderCatalog(ctx, &dtosystem.SystemProviderCatalogReq{Page: 1, PageSize: 10})
	if err != nil || providers.Total != 2 || len(providers.Items) != 2 {
		t.Fatalf("provider catalog=%+v err=%v", providers, err)
	}
	if providers.Items[0].ID != "anthropic" || providers.Items[1].ID != "openai" {
		t.Fatalf("provider catalog must be A→Z: %+v", providers.Items)
	}
	if providers.Items[0].Name != "Anthropic" || providers.Items[0].API != "https://api.anthropic.com/v1" || providers.Items[0].NPM != "@ai-sdk/anthropic" || providers.Items[0].ModelCount != 1 {
		t.Fatalf("provider catalog item=%+v", providers.Items[0])
	}
	search, err := (&SystemSvc{}).ProviderCatalog(ctx, &dtosystem.SystemProviderCatalogReq{Keyword: "openai", Page: 1, PageSize: 10})
	if err != nil || search.Total != 1 || search.Items[0].ID != "openai" {
		t.Fatalf("provider catalog search=%+v err=%v", search, err)
	}
}

// 模型目录里没有对应提供商条目时回退为目录键前缀，不能丢掉名称。
func TestModelCatalogProviderNameFallback(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api.json" {
			fmt.Fprint(w, `{"openai":{"id":"openai","name":"OpenAI","api":"https://api.openai.com/v1","models":{}}}`)
			return
		}
		fmt.Fprint(w, `{"swiss-ai/apertus-8b":{"id":"swiss-ai/apertus-8b","name":"Apertus 8B","limit":{"context":65536,"output":8192}}}`)
	}))
	defer server.Close()
	if err := db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).
		SetProviderSyncURL(server.URL + "/api.json").SetModelSyncURL(server.URL + "/models.json").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := NewModelCatalogSyncer(server.Client()).Sync(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := (&SystemSvc{}).ModelCatalog(ctx, &dtosystem.SystemModelCatalogReq{Page: 1, PageSize: 10})
	if err != nil || page.Total != 1 || page.Items[0].ProviderName != "swiss-ai" || page.Items[0].ModelID != "apertus-8b" {
		t.Fatalf("catalog=%+v err=%v", page, err)
	}
}

// 任一目录下载失败都必须整体失败并保留已有缓存，不能只更新一半。
func TestModelCatalogSyncFailurePreservesCatalog(t *testing.T) {
	tests := []struct {
		name           string
		providerStatus int
		modelStatus    int
	}{
		{name: "provider-fails", providerStatus: http.StatusServiceUnavailable, modelStatus: http.StatusOK},
		{name: "model-fails", providerStatus: http.StatusOK, modelStatus: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := setupSystemServiceTest(t)
			if err := db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).
				SetModelCatalogJSON(`[{"id":"saved","name":"Saved"}]`).SetProviderCatalogJSON(`[{"id":"saved","name":"Saved"}]`).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				status := tt.providerStatus
				body := `{"openai":{"id":"openai","name":"OpenAI","api":"https://api.openai.com/v1","models":{}}}`
				if r.URL.Path == "/models.json" {
					status = tt.modelStatus
					body = `{"openai/gpt-test":{"id":"openai/gpt-test","name":"GPT Test"}}`
				}
				w.WriteHeader(status)
				if status == http.StatusOK {
					fmt.Fprint(w, body)
				}
			}))
			defer server.Close()

			if err := db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).
				SetProviderSyncURL(server.URL + "/api.json").SetModelSyncURL(server.URL + "/models.json").Exec(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := NewModelCatalogSyncer(server.Client()).Sync(context.Background()); err == nil {
				t.Fatal("expected sync failure")
			}
			row, err := db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
			if err != nil || row.ModelCatalogJSON != `[{"id":"saved","name":"Saved"}]` || row.ProviderCatalogJSON != `[{"id":"saved","name":"Saved"}]` || row.ModelSyncLastAttemptAt == nil || row.ModelSyncLastError == "" {
				t.Fatalf("row=%+v err=%v", row, err)
			}
		})
	}
}
