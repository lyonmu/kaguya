package system

import (
	"time"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/ent"
)

type SystemAccessLogResp struct {
	ID                   string `json:"id"`                               // ID
	AccessIP             string `json:"access_ip"`                        // 访问IP
	AccessTime           int64  `json:"access_time"`                      // 操作时间
	Os                   string `json:"os,omitempty"`                     // 操作系统
	Platform             string `json:"platform,omitempty"`               // 操作平台
	BrowserName          string `json:"browser_name,omitempty"`           // 浏览器名称
	BrowserVersion       string `json:"browser_version,omitempty"`        // 浏览器版本
	BrowserEngineName    string `json:"browser_engine_name,omitempty"`    // 浏览器引擎名称
	BrowserEngineVersion string `json:"browser_engine_version,omitempty"` // 浏览器引擎版本
}

func (r *SystemAccessLogResp) LoadDb(e *ent.KaguyaAccessLog) {
	r.ID = e.ID
	r.AccessIP = e.AccessIP
	r.AccessTime = e.AccessTime
	r.Os = e.Os
	r.Platform = e.Platform
	r.BrowserName = e.BrowserName
	r.BrowserVersion = e.BrowserVersion
	r.BrowserEngineName = e.BrowserEngineName
	r.BrowserEngineVersion = e.BrowserEngineVersion
}

type SystemAccessLogListResp struct {
	Total    int                    `json:"total,omitempty"`     // 总条数
	Items    []*SystemAccessLogResp `json:"items,omitempty"`     // 操作日志列表
	Page     int                    `json:"page,omitempty"`      // 页码
	PageSize int                    `json:"page_size,omitempty"` // 每页条数
}

// SystemProviderResp 提供商详情。
type SystemProviderResp struct {
	ID           string                  `json:"id"`
	ProviderName string                  `json:"provider_name"`
	APIProtocol  consts.ProviderProtocol `json:"api_protocol"`
	APIKey       string                  `json:"api_key"`
	BaseURL      string                  `json:"base_url"`
	Models       []*SystemModelResp      `json:"models"`
	CreatedAt    time.Time               `json:"created_at"`
	UpdatedAt    time.Time               `json:"updated_at"`
}

func (r *SystemProviderResp) LoadDb(e *ent.KaguyaProviderInfo) {
	r.ID = e.ID
	r.ProviderName = e.ProviderName
	r.APIProtocol = e.APIProtocol
	r.APIKey = e.APIKey
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
	ID                         string                 `json:"id"`
	ProviderID                 string                 `json:"provider_id"`
	ProviderName               string                 `json:"provider_name"`
	ModelName                  string                 `json:"model_name"`
	ModelID                    string                 `json:"model_id"`
	IsDefault                  consts.Status          `json:"is_default"`
	ReasoningEnabled           consts.Status          `json:"reasoning_enabled"`
	ReasoningEffort            consts.ReasoningEffort `json:"reasoning_effort"`
	TokenContextWindow         int                    `json:"token_context_window"`
	TokenMaxOutputTokens       int                    `json:"token_max_output_tokens"`
	CapabilityToolUse          consts.Status          `json:"capability_tool_use"`
	CapabilityVision           consts.Status          `json:"capability_vision"`
	CapabilityStructuredOutput consts.Status          `json:"capability_structured_output"`
	CreatedAt                  time.Time              `json:"created_at"`
	UpdatedAt                  time.Time              `json:"updated_at"`
}

func (r *SystemModelResp) LoadDb(e *ent.KaguyaModelsInfo) {
	r.ID = e.ID
	r.ProviderID = e.ProviderID
	r.ModelName = e.ModelName
	r.ModelID = e.ModelID
	r.IsDefault = e.IsDefault
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

type SystemModelLabelResp struct {
	Label      string `json:"label"`
	Value      string `json:"value"`
	ProviderID string `json:"provider_id"`
	ModelID    string `json:"model_id"`
}
