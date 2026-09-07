package system

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSystemAccessLogPageReqBindQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(
		http.MethodGet,
		"/?access_ip=192.168.1.10&start_time=100&end_time=200&page=2&page_size=20",
		nil,
	)

	var req SystemAccessLogPageReq
	if err := ctx.ShouldBindQuery(&req); err != nil {
		t.Fatalf("bind query failed: %v", err)
	}

	if req.AccessIP != "192.168.1.10" {
		t.Fatalf("unexpected access IP: %q", req.AccessIP)
	}
	if req.StartTime != 100 || req.EndTime != 200 {
		t.Fatalf("unexpected time range: %d-%d", req.StartTime, req.EndTime)
	}
	if req.Page != 2 || req.PageSize != 20 {
		t.Fatalf("unexpected pagination: page=%d page_size=%d", req.Page, req.PageSize)
	}
}
