package system

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/lyonmu/kaguya/internal/consts"
)

func TestProviderTypeValidation(t *testing.T) {
	for _, kind := range []consts.ProviderType{"", consts.ProviderTypeNormal, consts.ProviderTypeOpenCodeGo, "unsupported"} {
		req := SystemProviderSaveReq{ProviderName: "test", APIProtocol: consts.ProtocolOpenAIChat, ProviderType: kind, BaseURL: "https://example.com/v1/chat/completions"}
		err := binding.Validator.ValidateStruct(req)
		if (err != nil) != (kind == "unsupported") {
			t.Errorf("provider type %q: %v", kind, err)
		}
	}
}

func TestProviderRequestURLValidation(t *testing.T) {
	for _, tt := range []struct {
		url   string
		valid bool
	}{
		{"", false}, {"/v1/responses", false}, {"example.com/api", false}, {"ftp://example.com/api", false},
		{"https://example.com/go/v1/chat/completions", true}, {"http://localhost:8080/custom?version=1", true},
	} {
		req := SystemProviderSaveReq{ProviderName: "test", APIProtocol: consts.ProtocolOpenAIChat, BaseURL: tt.url}
		err := binding.Validator.ValidateStruct(req)
		if (err == nil) != tt.valid {
			t.Errorf("request URL %q: %v", tt.url, err)
		}
	}
}

func TestModelNoLongerRequiresSelectionFlags(t *testing.T) {
	req := SystemModelSaveReq{
		ProviderID: "p", ModelName: "test", ModelID: "test",
		ReasoningEnabled: consts.IsTrue, ReasoningEffort: consts.ReasoningEffortMedium,
		CapabilityToolUse: consts.IsTrue, CapabilityVision: consts.IsTrue, CapabilityStructuredOutput: consts.IsTrue,
	}
	if err := binding.Validator.ValidateStruct(req); err != nil {
		t.Fatal(err)
	}
}

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
