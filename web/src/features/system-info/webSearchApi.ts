import { get, guardFailure, guardSuccess, isBoolean, isRecord, put } from '../../api/http'

export interface WebSearchConfig {
  enabled: boolean
  key_configured: boolean
}

export interface WebSearchPayload {
  enabled: boolean
  api_key?: string
}

const PATH = '/v1/system/web-search'

function parseConfig(data: unknown) {
  if (!isRecord(data) || !isBoolean(data.enabled) || !isBoolean(data.key_configured)) return guardFailure('联网搜索配置格式无效')
  return guardSuccess({ enabled: data.enabled, key_configured: data.key_configured })
}

export function fetchWebSearchConfig(signal: AbortSignal) {
  return get<WebSearchConfig>(PATH, undefined, signal, parseConfig)
}

export function updateWebSearchConfig(payload: WebSearchPayload) {
  return put<WebSearchConfig>(PATH, payload, undefined, parseConfig)
}
