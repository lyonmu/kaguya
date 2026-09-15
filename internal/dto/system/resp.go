package system

import (
	"time"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/secret"
)

// SystemProviderResp 提供商响应。APIKey 始终是掩码，明文只能通过单独的查看接口获取。
type SystemProviderResp struct {
	ProviderType consts.ProviderType `json:"provider_type"`
	ID           string              `json:"id"`
	ProviderName string              `json:"provider_name"`
	APIKey       string              `json:"api_key"`
	APIKeySet    bool                `json:"api_key_set"`
	BaseURL      string              `json:"base_url"`
	Models       []*SystemModelResp  `json:"models"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

// SystemProviderAPIKeyResp 单个提供商的 API Key 明文，只在显式查看时返回。
type SystemProviderAPIKeyResp struct {
	ID     string `json:"id"`
	APIKey string `json:"api_key"`
}

func (r *SystemProviderResp) LoadDb(e *ent.KaguyaProviderInfo) {
	r.ID = e.ID
	r.ProviderName = e.ProviderName
	r.ProviderType = e.ProviderType
	// 掩码只保留首尾各四位便于人工辨识，无法反推明文；密钥材料不匹配或
	// 密文损坏时退回统一占位，不阻断列表。
	if plain, err := secret.Decrypt(e.APIKey); err == nil {
		r.APIKey = secret.Mask(plain)
	} else {
		r.APIKey = secret.MaskUnavailable
	}
	r.APIKeySet = e.APIKey != ""
	r.BaseURL = e.BaseURL
	r.CreatedAt = e.CreatedAt
	r.UpdatedAt = e.UpdatedAt
	r.Models = make([]*SystemModelResp, 0, len(e.Edges.Models))
	for _, model := range e.Edges.Models {
		item := &SystemModelResp{}
		item.LoadDb(model)
		item.ProviderName = e.ProviderName
		r.Models = append(r.Models, item)
	}
}

type SystemProviderListResp struct {
	Total    int                   `json:"total"`
	Items    []*SystemProviderResp `json:"items"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"page_size"`
}

type SystemProviderLabelResp struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// SystemModelResp 模型详情。
type SystemModelResp struct {
	ID                         string                  `json:"id"`
	ProviderID                 string                  `json:"provider_id"`
	ProviderName               string                  `json:"provider_name"`
	ModelName                  string                  `json:"model_name"`
	ModelID                    string                  `json:"model_id"`
	APIProtocol                consts.ProviderProtocol `json:"api_protocol"`
	RequestPath                string                  `json:"request_path"`
	ReasoningEnabled           consts.Status           `json:"reasoning_enabled"`
	ReasoningEffort            consts.ReasoningEffort  `json:"reasoning_effort"`
	TokenContextWindow         int                     `json:"token_context_window"`
	TokenMaxOutputTokens       int                     `json:"token_max_output_tokens"`
	CapabilityToolUse          consts.Status           `json:"capability_tool_use"`
	CapabilityVision           consts.Status           `json:"capability_vision"`
	CapabilityStructuredOutput consts.Status           `json:"capability_structured_output"`
	CreatedAt                  time.Time               `json:"created_at"`
	UpdatedAt                  time.Time               `json:"updated_at"`
}

func (r *SystemModelResp) LoadDb(e *ent.KaguyaModelsInfo) {
	r.ID = e.ID
	r.ProviderID = e.ProviderID
	r.ModelName = e.ModelName
	r.ModelID = e.ModelID
	r.APIProtocol = e.APIProtocol
	r.RequestPath = e.RequestPath
	r.ReasoningEnabled = e.ReasoningEnabled
	r.ReasoningEffort = e.ReasoningEffort
	r.TokenContextWindow = e.TokenContextWindow
	r.TokenMaxOutputTokens = e.TokenMaxOutputTokens
	r.CapabilityToolUse = e.CapabilityToolUse
	r.CapabilityVision = e.CapabilityVision
	r.CapabilityStructuredOutput = e.CapabilityStructuredOutput
	r.CreatedAt = e.CreatedAt
	r.UpdatedAt = e.UpdatedAt
	if e.Edges.Provider != nil {
		r.ProviderName = e.Edges.Provider.ProviderName
	}
}

type SystemModelListResp struct {
	Total    int                `json:"total"`
	Items    []*SystemModelResp `json:"items"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
}

// SystemModelTestResp 模型测试结果：直接回传模型的应答原文与耗时。
type SystemModelTestResp struct {
	Reply      string `json:"reply"`
	DurationMS int64  `json:"duration_ms"`
}

type SystemModelLabelResp struct {
	ProviderName string `json:"provider_name"`
	Label        string `json:"label"`
	Value        string `json:"value"`
	ProviderID   string `json:"provider_id"`
	ModelID      string `json:"model_id"`
	IsDefault    bool   `json:"is_default"`
}

// SystemModelCatalogResp 是 models.dev models.json 中的一项，已按模型去重；
// ID 是目录键（提供商前缀 + 模型标识），ModelID 才是上游 API 模型标识。
type SystemModelCatalogResp struct {
	ID                         string        `json:"id"`
	ProviderID                 string        `json:"provider_id"`
	ProviderName               string        `json:"provider_name"`
	ModelID                    string        `json:"model_id"`
	Name                       string        `json:"name"`
	Family                     string        `json:"family"`
	Description                string        `json:"description"`
	ReasoningEnabled           consts.Status `json:"reasoning_enabled"`
	TokenContextWindow         int           `json:"token_context_window"`
	TokenMaxOutputTokens       int           `json:"token_max_output_tokens"`
	CapabilityToolUse          consts.Status `json:"capability_tool_use"`
	CapabilityVision           consts.Status `json:"capability_vision"`
	CapabilityStructuredOutput consts.Status `json:"capability_structured_output"`
	InputModalities            []string      `json:"input_modalities"`
	ReleaseDate                string        `json:"release_date"`
	LastUpdated                string        `json:"last_updated"`
}

type SystemModelCatalogListResp struct {
	Total    int                       `json:"total"`
	Items    []*SystemModelCatalogResp `json:"items"`
	Page     int                       `json:"page"`
	PageSize int                       `json:"page_size"`
}

type SystemModelSyncResp struct {
	Count         int       `json:"count"`
	ProviderCount int       `json:"provider_count"`
	SyncedAt      time.Time `json:"synced_at"`
}

// SystemProviderCatalogResp 是 models.dev 提供商目录中的一项，用于预填新建提供商；
// 只包含带 api 根地址的提供商，没有 api 的提供商只出现在模型目录中。
type SystemProviderCatalogResp struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	API        string `json:"api"`
	NPM        string `json:"npm"`
	Doc        string `json:"doc"`
	ModelCount int    `json:"model_count"`
}

type SystemProviderCatalogListResp struct {
	Total    int                          `json:"total"`
	Items    []*SystemProviderCatalogResp `json:"items"`
	Page     int                          `json:"page"`
	PageSize int                          `json:"page_size"`
}
