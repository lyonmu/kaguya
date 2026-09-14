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

func TestProviderRequestURLValidation(t *testing.T) {
	for _, tt := range []struct {
		url   string
		valid bool
	}{
		{"", false}, {"/v1/responses", false}, {"example.com/api", false}, {"ftp://example.com/api", false},
		{"https://example.com/go/v1", true}, {"http://localhost:8080/custom", true},
	} {
		req := SystemProviderSaveReq{ProviderName: "test", BaseURL: tt.url}
		err := binding.Validator.ValidateStruct(req)
		if (err == nil) != tt.valid {
			t.Errorf("request URL %q: %v", tt.url, err)
		}
	}
}

func TestModelNoLongerRequiresSelectionFlags(t *testing.T) {
	req := SystemModelSaveReq{
		ProviderID: "p", ModelName: "test", ModelID: "test", APIProtocol: consts.ProtocolOpenAIChat,
		ReasoningEnabled: consts.IsTrue, ReasoningEffort: consts.ReasoningEffortMedium,
		CapabilityToolUse: consts.IsTrue, CapabilityVision: consts.IsTrue, CapabilityStructuredOutput: consts.IsTrue,
	}
	if err := binding.Validator.ValidateStruct(req); err != nil {
		t.Fatal(err)
	}
}
