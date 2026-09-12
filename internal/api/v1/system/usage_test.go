package system

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/gin-gonic/gin"
	"github.com/lyonmu/kaguya/internal/db"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
)

func TestTokenUsageAPI(t *testing.T) {
	client, err := ent.Open(dialect.SQLite, "file:usage-api?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Schema.Create(context.Background(), migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	previous := db.EntClient
	db.EntClient = client
	defer func() { db.EntClient = previous }()
	router := gin.New()
	router.GET("/usage", (&SystemApiV1Group{}).SystemTokenUsage)
	for _, tc := range []struct {
		query string
		code  int
	}{
		{"?start_time=1735689600&end_time=1735948799", dtocode.SystemSuccess.Code},
		{"?start=2025-01-01&end=2025-01-03", dtocode.RequestParameterError.Code},
		{"?start_time=2025-01-01", dtocode.RequestParameterError.Code},
		{"?start_time=1735689600.5", dtocode.RequestParameterError.Code},
		{"?start_time=-1", dtocode.RequestParameterError.Code},
		{"?start_time=1735689600000&end_time=1735948799000", dtocode.RequestParameterError.Code},
		{"?start_time=1735776000&end_time=1735689600", dtocode.RequestParameterError.Code},
		{"?start_time=1577836800&end_time=1735689600", dtocode.RequestParameterError.Code},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/usage"+tc.query, nil))
		var response struct {
			Code int `json:"code"`
			Data struct {
				Days []any `json:"days"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Code != tc.code {
			t.Fatalf("%s: %s", tc.query, w.Body.String())
		}
		if tc.code == dtocode.SystemSuccess.Code && (len(response.Data.Days) != usageActivityDays() || w.Header().Get("Cache-Control") != "no-store") {
			t.Fatalf("bad response: %s", w.Body.String())
		}
	}
}

// usageActivityDays 复现固定的“最近一年”活动窗口天数：起点为今天 UTC 日初向前一年加一天，终点为今天，含首尾 365 或 366 天。
func usageActivityDays() int {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	return int(today.Sub(today.AddDate(-1, 0, 1))/(24*time.Hour)) + 1
}
