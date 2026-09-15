package system

import (
	"testing"

	"github.com/gin-gonic/gin/binding"
	"github.com/lyonmu/kaguya/internal/consts"
)

func TestProviderTypeValidation(t *testing.T) {
	for _, kind := range []consts.ProviderType{"", consts.ProviderTypeNormal, consts.ProviderTypeOpenCodeGo, "unsupported"} {
		req := SystemProviderSaveReq{ProviderName: "test", ProviderType: kind, BaseURL: "https://example.com/v1"}
		err := binding.Validator.ValidateStruct(req)
		if (err != nil) != (kind == "unsupported") {
			t.Errorf("provider type %q: %v", kind, err)
		}
	}
}

// BaseURL 只要求非空字符串，具体拼接结果在组装请求时校验。
func TestProviderBaseURLRequired(t *testing.T) {
	if err := binding.Validator.ValidateStruct(SystemProviderSaveReq{ProviderName: "test"}); err == nil {
		t.Error("empty base URL must be rejected")
	}
	for _, baseURL := range []string{"/v1/responses", "example.com/api", "ftp://example.com/api", "https://example.com/go/v1", "http://localhost:8080/custom"} {
		req := SystemProviderSaveReq{ProviderName: "test", BaseURL: baseURL}
		if err := binding.Validator.ValidateStruct(req); err != nil {
			t.Errorf("base URL %q: %v", baseURL, err)
		}
	}
}

func TestModelNoLongerRequiresSelectionFlags(t *testing.T) {
	req := SystemModelSaveReq{
		ProviderID: "p", ModelName: "test", ModelID: "test", APIProtocol: consts.ProtocolOpenAIChat, RequestPath: "/v1/chat/completions",
		ReasoningEnabled: consts.IsTrue, ReasoningEffort: consts.ReasoningEffortMedium,
		CapabilityToolUse: consts.IsTrue, CapabilityVision: consts.IsTrue, CapabilityStructuredOutput: consts.IsTrue,
	}
	if err := binding.Validator.ValidateStruct(req); err != nil {
		t.Fatal(err)
	}
	// request_path 必填，缺少时在绑定阶段就被拒绝。
	req.RequestPath = ""
	if err := binding.Validator.ValidateStruct(req); err == nil {
		t.Fatal("missing request path must be rejected")
	}
}
