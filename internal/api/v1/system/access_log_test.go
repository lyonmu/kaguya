package system

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	"github.com/lyonmu/kaguya/internal/global"
	"go.uber.org/zap"
)

func TestAccessLogRejectsInvalidTimeRequests(t *testing.T) {
	oldLogger := global.Logger
	global.Logger = zap.NewNop()
	defer func() { global.Logger = oldLogger }()
	router := gin.New()
	router.GET("/accesslog", (&SystemApiV1Group{}).SystemAccessLogPage)
	for _, query := range []string{
		"start_time=2025-01-01", "start_time=1735689600.1", "start_time=-1", "end_time=-1",
		"start_time=1735689600000", "end_time=1735689600000", "start_time=200&end_time=100",
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/accesslog?page=1&page_size=10&"+query, nil))
		var response struct {
			Code int `json:"code"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Code != dtocode.RequestParameterError.Code {
			t.Fatalf("%s: %s", query, w.Body.String())
		}
	}
}
