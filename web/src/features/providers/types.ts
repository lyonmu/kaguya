export type ProviderProtocol = 'openai-chat' | 'anthropic' | 'openai-response'
export type ProviderType = 'normal' | 'opencode-go'
export type Status = 1 | 2
export type ReasoningEffort = 'low' | 'medium' | 'high'

export interface AIModel {
  id: string
  provider_id: string
  provider_name: string
  model_name: string
  model_id: string
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
  /** 后端只返回掩码，明文需调用 fetchProviderAPIKey 单独获取。 */
  api_key: string
  api_key_set: boolean
  base_url: string
  models: AIModel[]
  created_at: string
  updated_at: string
}

export interface ProviderAPIKeyResponse {
  id: string
  api_key: string
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
  /** 留空表示保留已存储的密钥；后端不会回传明文供表单预填。 */
  api_key: string
  base_url: string
}

export interface ModelPayload {
  provider_id: string
  model_name: string
  model_id: string
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
  is_default: boolean
}

export interface LabelOption {
  label: string
  value: string
}

export interface ModelCatalogItem {
  id: string
  name: string
  lab: string
  family: string
  description: string
  reasoning_enabled: Status
  token_context_window: number
  token_max_output_tokens: number
  capability_tool_use: Status
  capability_vision: Status
  capability_structured_output: Status
  input_modalities: string[]
  release_date: string
  last_updated: string
}

export interface ModelCatalogResponse {
  total: number
  items: ModelCatalogItem[]
  page: number
  page_size: number
}

export interface ModelSyncResponse {
  count: number
  synced_at: string
}
