package system

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/gin-gonic/gin"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	"github.com/lyonmu/kaguya/internal/global"
	"github.com/lyonmu/kaguya/internal/secret"
	"go.uber.org/zap"
)

func TestWebSearchConfigurationAPI(t *testing.T) {
	ctx := t.Context()
	client, err := ent.Open(dialect.SQLite, "file:web-search-api?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.Create().SetSystemPrompt("keep this setting").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	oldClient, oldLogger := db.EntClient, global.Logger
	db.EntClient, global.Logger = client, zap.NewNop()
	defer func() { db.EntClient, global.Logger = oldClient, oldLogger }()
	defer secret.Reset()

	const path = "/v1/system/web-search"
	const key = "search-api-test-key"
	api, router := &SystemApiV1Group{}, gin.New()
	router.GET(path, api.SystemWebSearch)
	router.PUT(path, api.SystemWebSearchUpdate)

	request := func(method, body string, code int, want dtosystem.WebSearchConfig) {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, r)
		var response struct {
			Code int                       `json:"code"`
			Data dtosystem.WebSearchConfig `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Code != code || response.Data != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s response code=%d data=%+v", method, response.Code, response.Data)
		}
		if strings.Contains(w.Body.String(), key) || strings.Contains(w.Body.String(), secret.CiphertextPrefixV2) || strings.Contains(w.Body.String(), `"api_key"`) {
			t.Fatal("response included the API key")
		}
	}
	ok, invalid := dtocode.SystemSuccess.Code, dtocode.RequestParameterError.Code
	empty := dtosystem.WebSearchConfig{}
	request(http.MethodGet, "", ok, empty)
	request(http.MethodPut, `{}`, invalid, empty)
	request(http.MethodPut, `{"enabled":true}`, invalid, empty)
	request(http.MethodPut, `{"enabled":true,"api_key":"a b"}`, invalid, empty)
	request(http.MethodPut, `{"enabled":true,"api_key":"`+key+`"}`, dtocode.SystemInfoUpdateFailure.Code, empty)
	request(http.MethodGet, "", ok, empty)

	if err := secret.Init(strings.Repeat("ab", 32), nil); err != nil {
		t.Fatal(err)
	}
	enabled := dtosystem.WebSearchConfig{Enabled: true, KeyConfigured: true}
	disabled := dtosystem.WebSearchConfig{KeyConfigured: true}
	request(http.MethodPut, `{"enabled":true,"api_key":"`+key+`"}`, ok, enabled)
	request(http.MethodPut, `{"enabled":false}`, ok, disabled)
	request(http.MethodGet, "", ok, disabled)
	request(http.MethodPut, `{"enabled":true,"api_key":""}`, ok, enabled)

	row, err := client.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil {
		t.Fatal(err)
	}
	plain, decryptErr := secret.Decrypt(row.WebSearchAPIKey)
	if decryptErr != nil || !secret.IsEncrypted(row.WebSearchAPIKey) || plain != key || !row.WebSearchEnabled || row.SystemPrompt != "keep this setting" {
		t.Fatal("configuration did not preserve encryption, the key, or unrelated fields")
	}
}
