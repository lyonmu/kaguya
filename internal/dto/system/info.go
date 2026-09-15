package system

import "time"

// SystemInfoSaveReq 整体保存系统配置；空模型 ID 表示取消选择。
type SystemInfoSaveReq struct {
	ContextCompactionPercent *int     `json:"context_compaction_percent,omitempty" binding:"omitempty,min=10,max=95"`
	AgentMaxSteps            *int     `json:"agent_max_steps,omitempty" binding:"omitempty,min=0,max=1000"`
	CommandTimeoutSeconds    *int     `json:"command_timeout_seconds,omitempty" binding:"omitempty,min=1,max=86400"`
	ChatMaxRetries           *int     `json:"chat_max_retries,omitempty" binding:"omitempty,min=0,max=20"`
	GlobalAgentsPaths        []string `json:"global_agents_paths"`
	GlobalSystemPrompt       *string  `json:"global_system_prompt,omitempty" binding:"omitempty,max=20000"`
	SystemPrompt             string   `json:"system_prompt" binding:"max=20000"`
	ModelSyncEnabled         bool     `json:"model_sync_enabled"`
	ModelSyncURL             string   `json:"model_sync_url" binding:"omitempty,max=2048"`
	ProviderSyncURL          string   `json:"provider_sync_url" binding:"omitempty,max=2048"`
	ModelSyncIntervalHours   *int     `json:"model_sync_interval_hours,omitempty" binding:"omitempty,min=1,max=720"`
	DefaultModelID           string   `json:"default_model_id" binding:"max=64"`
	TaskModelID              string   `json:"task_model_id" binding:"max=64"`
}

type SystemInfoResp struct {
	SystemInfoSaveReq
	ModelSyncCatalogCount  int        `json:"model_sync_catalog_count"`
	ProviderCatalogCount   int        `json:"provider_catalog_count"`
	ModelSyncLastAttemptAt *time.Time `json:"model_sync_last_attempt_at,omitempty"`
	ModelSyncLastSuccessAt *time.Time `json:"model_sync_last_success_at,omitempty"`
	ModelSyncLastError     string     `json:"model_sync_last_error"`
}
