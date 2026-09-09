export type ProviderProtocol = 'openai-chat' | 'anthropic' | 'openai-response'
export type ProviderType = 'normal' | 'opencode-go'
export type Status = 1 | 2
export type ReasoningEffort = 'low' | 'medium' | 'high'

export interface AIModel {
  is_task: Status
  id: string
  provider_id: string
  provider_name: string
  model_name: string
  model_id: string
  is_default: Status
  reasoning_enabled: Status
  reasoning_effort: ReasoningEffort
  token_context_window: number
  token_max_output_tokens: number
  capability_tool_use: Status
  capability_vision: Status
  capability_structured_output: Status
  created_at: string
  updated_at: string
}

export interface AIProvider {
  provider_type: ProviderType
  id: string
  provider_name: string
  api_protocol: ProviderProtocol
  api_key: string
  base_url: string
  models: AIModel[]
  created_at: string
  updated_at: string
}

export interface ProviderPageResponse {
  total: number
  items: AIProvider[]
  page: number
  page_size: number
}

export interface ProviderQuery {
  providerName?: string
  apiProtocol?: ProviderProtocol
  page: number
  pageSize: number
}

export interface ProviderPayload {
  provider_type: ProviderType
  provider_name: string
  api_protocol: ProviderProtocol
  api_key: string
  base_url: string
}

export interface ModelPayload {
  is_task: Status
  provider_id: string
  model_name: string
  model_id: string
  is_default: Status
  reasoning_enabled: Status
  reasoning_effort: ReasoningEffort
  token_context_window: number
  token_max_output_tokens: number
  capability_tool_use: Status
  capability_vision: Status
  capability_structured_output: Status
}

export interface ModelLabelOption {
  label: string
  value: string
  provider_id: string
  provider_name: string
  model_id: string
}

export interface LabelOption {
  label: string
  value: string
}
