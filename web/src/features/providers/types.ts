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
  /** 请求协议，决定运行时使用哪套请求实现。 */
  api_protocol: ProviderProtocol
  /** 模型请求路径，与提供商 base_url 拼接成最终请求地址。 */
  request_path: string
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
  /** 后端只返回掩码，明文需调用 fetchProviderAPIKey 单独获取。 */
  api_key: string
  api_key_set: boolean
  /** 提供商 BaseURL，请求时与模型的 request_path 拼接。 */
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
  page: number
  pageSize: number
}

export interface ProviderPayload {
  provider_type: ProviderType
  provider_name: string
  /** 留空表示保留已存储的密钥；后端不会回传明文供表单预填。 */
  api_key: string
  /** 提供商 BaseURL，例如 https://api.deepseek.com。 */
  base_url: string
}

export interface ModelPayload {
  provider_id: string
  model_name: string
  model_id: string
  api_protocol: ProviderProtocol
  request_path: string
  reasoning_enabled: Status
  reasoning_effort: ReasoningEffort
  token_context_window: number
  token_max_output_tokens: number
  capability_tool_use: Status
  capability_vision: Status
  capability_structured_output: Status
}

/** 模型测试结果：reply 是模型应答原文，duration_ms 是本次请求耗时。 */
export interface ModelTestResponse {
  reply: string
  duration_ms: number
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
  /** 目录内全局唯一键（提供商前缀 + 模型标识）。 */
  id: string
  provider_id: string
  provider_name: string
  /** 去掉提供商前缀的模型标识。 */
  model_id: string
  name: string
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
  provider_count: number
  synced_at: string
}

/** models.dev 提供商目录项，用于预填新建提供商。 */
export interface ProviderCatalogItem {
  id: string
  name: string
  api: string
  npm: string
  doc: string
  model_count: number
}

export interface ProviderCatalogResponse {
  total: number
  items: ProviderCatalogItem[]
  page: number
  page_size: number
}
