package system

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/gin-gonic/gin"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	"github.com/lyonmu/kaguya/internal/global"
	initialize "github.com/lyonmu/kaguya/internal/init"
	"go.uber.org/zap"
)

func TestSystemInfoAPI(t *testing.T) {
	ctx := context.Background()
	client, err := ent.Open(dialect.SQLite, "file:system-info-api?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldLogger := db.EntClient, global.Logger
	db.EntClient, global.Logger = client, zap.NewNop()
	defer func() { db.EntClient, global.Logger = oldClient, oldLogger }()
	if err := client.KaguyaProviderInfo.Create().SetID("p").SetProviderName("p").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaModelsInfo.Create().SetID("m").SetProviderID("p").SetModelName("m").SetModelID("api-m").SetIsDefault(consts.IsFalse).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := initialize.Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	api, router := &SystemApiV1Group{}, gin.New()
	router.GET("/v1/system/info", api.SystemInfo)
	router.PUT("/v1/system/info", api.SystemInfoUpdate)
	request := func(method, body string, code int) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/v1/system/info", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, r)
		var response struct {
			Code int            `json:"code"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Code != code {
			t.Fatalf("%s: %s", method, w.Body.String())
		}
		if method == "GET" && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("config response must not be cached")
		}
		return response.Data
	}
	ok, bad := dtocode.SystemSuccess.Code, dtocode.RequestParameterError.Code
	info := request("GET", "", ok)
	if info["user_agent"] != consts.DefaultUserAgent || info["global_system_prompt"] != consts.GlobalSystemPrompt {
		t.Fatalf("defaults=%+v", info)
	}
	request("PUT", `{}`, bad)
	request("PUT", `{"user_agent":"bad\r\nHeader: value"}`, bad)
	request("PUT", `{"user_agent":"agent","task_model_id":"missing"}`, dtocode.ModelNotFound.Code)
	request("PUT", `{"system_prompt":"用中文回答","user_agent":"Agent/2","default_model_id":"m","task_model_id":"m","global_system_prompt":"cannot override"}`, ok)
	info = request("GET", "", ok)
	if info["system_prompt"] != "用中文回答" || info["user_agent"] != "Agent/2" || info["default_model_id"] != "m" || info["task_model_id"] != "m" || info["global_system_prompt"] != consts.GlobalSystemPrompt {
		t.Fatalf("saved=%+v", info)
	}
	request("PUT", `{"user_agent":"Agent/3"}`, ok)
	info = request("GET", "", ok)
	if info["default_model_id"] != "" || info["task_model_id"] != "" {
		t.Fatalf("clear=%+v", info)
	}
}
