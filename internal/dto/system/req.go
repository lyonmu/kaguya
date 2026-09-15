package system

import "github.com/lyonmu/kaguya/internal/consts"

// SystemIDReq 通用资源 ID 请求。
type SystemIDReq struct {
	ID string `uri:"id" binding:"required"`
}

// SystemProviderSaveReq 提供商新增/修改请求。
// 协议不在这里：同一提供商可提供多个协议，由每个模型自己声明。
type SystemProviderSaveReq struct {
	ProviderType consts.ProviderType `json:"provider_type" binding:"omitempty,oneof=normal opencode-go"`
	ProviderName string              `json:"provider_name" binding:"required"` // 提供商名称
	APIKey       string              `json:"api_key"`                          // API Key
	BaseURL      string              `json:"base_url" binding:"required"`      // 提供商 BaseURL，请求时与模型的 request_path 拼接
}

// SystemProviderPageReq 提供商分页查询请求。
type SystemProviderPageReq struct {
	ProviderName string `json:"provider_name,omitempty" form:"provider_name"`
	Page         int    `json:"page,omitempty" form:"page" binding:"required,min=1" minimum:"1" default:"1"`
	PageSize     int    `json:"page_size,omitempty" form:"page_size" binding:"required,min=10,max=1000" minimum:"10" maximum:"1000" default:"10"`
}

// SystemProviderLabelReq 提供商下拉选项查询请求。
type SystemProviderLabelReq struct {
	Keyword string `json:"keyword,omitempty" form:"keyword"`
}

// SystemModelSaveReq 模型新增/修改请求。
type SystemModelSaveReq struct {
	ProviderID                 string                  `json:"provider_id" binding:"required"`
	ModelName                  string                  `json:"model_name" binding:"required"`
	ModelID                    string                  `json:"model_id" binding:"required"`
	APIProtocol                consts.ProviderProtocol `json:"api_protocol" binding:"required,oneof=openai-chat anthropic openai-response"` // 请求协议，决定运行时使用哪套请求实现
	RequestPath                string                  `json:"request_path" binding:"required"`                                             // 模型请求路径，与提供商 BaseURL 拼接成最终请求地址
	ReasoningEnabled           consts.Status           `json:"reasoning_enabled" binding:"required,oneof=1 2"`
	ReasoningEffort            consts.ReasoningEffort  `json:"reasoning_effort" binding:"required,oneof=minimal low medium high xhigh max"`
	TokenContextWindow         int                     `json:"token_context_window" binding:"min=0"`
	TokenMaxOutputTokens       int                     `json:"token_max_output_tokens" binding:"min=0"`
	CapabilityToolUse          consts.Status           `json:"capability_tool_use" binding:"required,oneof=1 2"`
	CapabilityVision           consts.Status           `json:"capability_vision" binding:"required,oneof=1 2"`
	CapabilityStructuredOutput consts.Status           `json:"capability_structured_output" binding:"required,oneof=1 2"`
}

// SystemModelPageReq 模型分页查询请求。
type SystemModelPageReq struct {
	ProviderID string `json:"provider_id,omitempty" form:"provider_id"`
	Keyword    string `json:"keyword,omitempty" form:"keyword"`
	Page       int    `json:"page,omitempty" form:"page" binding:"required,min=1" minimum:"1" default:"1"`
	PageSize   int    `json:"page_size,omitempty" form:"page_size" binding:"required,min=10,max=1000" minimum:"10" maximum:"1000" default:"10"`
}

// SystemModelLabelReq 模型下拉选项查询请求；ProviderID 为空时返回全部模型。
type SystemModelLabelReq struct {
	ProviderID string `json:"provider_id,omitempty" form:"provider_id"`
	Keyword    string `json:"keyword,omitempty" form:"keyword"`
}

// SystemModelCatalogReq 查询已同步的 models.dev 模型目录。
type SystemModelCatalogReq struct {
	Keyword  string `json:"keyword,omitempty" form:"keyword"`
	Page     int    `json:"page,omitempty" form:"page" binding:"required,min=1" minimum:"1" default:"1"`
	PageSize int    `json:"page_size,omitempty" form:"page_size" binding:"required,min=10,max=1000" minimum:"10" maximum:"1000" default:"100"`
}

// SystemProviderCatalogReq 查询已同步的 models.dev 提供商目录。
type SystemProviderCatalogReq struct {
	Keyword  string `json:"keyword,omitempty" form:"keyword"`
	Page     int    `json:"page,omitempty" form:"page" binding:"required,min=1" minimum:"1" default:"1"`
	PageSize int    `json:"page_size,omitempty" form:"page_size" binding:"required,min=10,max=1000" minimum:"10" maximum:"1000" default:"100"`
}
