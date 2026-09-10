export interface SystemInfoPayload {
  context_compaction_percent?: number
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
  tls?: TLSInfo
}

export interface TLSInfo { certificate_pem: string; fingerprint: string; not_after: string; hosts: string[] }
export interface TLSPayload { generate?: boolean; hosts?: string[]; certificate_pem?: string; private_key_pem?: string }
