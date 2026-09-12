package system

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/gin-gonic/gin"
	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	"github.com/lyonmu/kaguya/internal/global"
	initialize "github.com/lyonmu/kaguya/internal/init"
	"github.com/lyonmu/kaguya/internal/secret"
	"go.uber.org/zap"
)

type cacheTestID struct{ value atomic.Int64 }

func (g *cacheTestID) GenID() (int64, error) { return g.value.Add(1), nil }

// 明文 API Key 与掩码列表响应都必须禁用缓存。
func TestProviderSecretResponsesDisableCaching(t *testing.T) {
	ctx := context.Background()
	client, err := ent.Open(dialect.SQLite, "file:provider-cache?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldLogger, oldID := db.EntClient, global.Logger, global.Id
	db.EntClient, global.Logger, global.Id = client, zap.NewNop(), &cacheTestID{}
	defer func() { db.EntClient, global.Logger, global.Id = oldClient, oldLogger, oldID }()
	if err := initialize.Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := secret.Init(strings.Repeat("ab", 32), ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secret.Reset)

	api, router := &SystemApiV1Group{}, gin.New()
	router.POST("/v1/system/provider", api.SystemProviderCreate)
	router.GET("/v1/system/provider/page", api.SystemProviderPage)
	router.GET("/v1/system/provider/:id/api-key", api.SystemProviderAPIKey)

	create := httptest.NewRecorder()
	createReq := httptest.NewRequest("POST", "/v1/system/provider", strings.NewReader(`{"provider_name":"p","api_protocol":"openai-chat","base_url":"https://api.example.com/v1/chat/completions","api_key":"sk-cache-test-1234"}`))
	createReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(create, createReq)
	if create.Code != 200 || create.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create status=%d cache=%q", create.Code, create.Header().Get("Cache-Control"))
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil || created.Data.ID == "" {
		t.Fatalf("create response=%s err=%v", create.Body.String(), err)
	}

	for _, endpoint := range []string{"/v1/system/provider/page?page=1&page_size=10", "/v1/system/provider/" + created.Data.ID + "/api-key"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", endpoint, nil))
		if w.Code != 200 {
			t.Fatalf("%s status=%d body=%s", endpoint, w.Code, w.Body.String())
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s cache=%q", endpoint, got)
		}
		if strings.Contains(w.Body.String(), "sk-cache-test-1234") && !strings.HasSuffix(endpoint, "/api-key") {
			t.Fatalf("list leaked plaintext: %s", w.Body.String())
		}
	}
}
