export interface SystemInfoPayload {
  agent_max_steps?: number
  command_timeout_seconds?: number
  global_agents_paths?: string[]
  system_prompt: string
  user_agent: string
  default_model_id: string
  task_model_id: string
}

export interface SystemInfo extends SystemInfoPayload {
  global_system_prompt: string
}
