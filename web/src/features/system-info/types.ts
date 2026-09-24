export interface SystemInfoPayload {
  context_compaction_percent?: number
  agent_max_steps?: number
  command_timeout_seconds?: number
  chat_max_retries?: number
  global_agents_paths?: string[]
  global_system_prompt: string
  system_prompt: string
  model_sync_enabled: boolean
  model_sync_url: string
  provider_sync_url: string
  model_sync_interval_hours?: number
  default_model_id: string
  task_model_id: string
  memory_enabled: boolean
  memory_auto_capture: boolean
  memory_context_tokens: number
}

export interface SystemInfo extends SystemInfoPayload {
  memory_policy_epoch: number
  model_sync_catalog_count: number
  provider_catalog_count: number
  model_sync_last_attempt_at?: string
  model_sync_last_success_at?: string
  model_sync_last_error: string
}
