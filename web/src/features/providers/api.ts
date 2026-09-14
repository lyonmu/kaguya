import { del, get, post, put } from '../../api/http'
import type {
  AIModel,
  AIProvider,
  LabelOption,
  ModelPayload,
  ModelLabelOption,
  ModelCatalogResponse,
  ModelSyncResponse,
  ProviderAPIKeyResponse,
  ProviderCatalogResponse,
  ProviderPageResponse,
  ProviderPayload,
  ProviderQuery,
} from './types'

const PROVIDER_PATH = '/v1/system/provider'
const MODEL_PATH = '/v1/system/model'

export function fetchProviders(query: ProviderQuery, signal?: AbortSignal) {
  return get<ProviderPageResponse>(
    `${PROVIDER_PATH}/page`,
    {
      provider_name: query.providerName,
      page: query.page,
      page_size: query.pageSize,
    },
    signal,
  )
}

export function fetchProviderLabels(signal?: AbortSignal) {
  return get<LabelOption[]>(`${PROVIDER_PATH}/label`, undefined, signal)
}

export function fetchProviderCatalogPage(keyword: string, page: number, pageSize: number, signal?: AbortSignal) {
  return get<ProviderCatalogResponse>(`${PROVIDER_PATH}/catalog`, { keyword, page, page_size: pageSize }, signal)
}

export function createProvider(payload: ProviderPayload) {
  return post<AIProvider>(PROVIDER_PATH, payload)
}

export function updateProvider(id: string, payload: ProviderPayload) {
  return put<AIProvider>(`${PROVIDER_PATH}/${id}`, payload)
}

/** 按需获取单个提供商的 API Key 明文，仅在用户显式查看时调用。 */
export function fetchProviderAPIKey(id: string, signal?: AbortSignal) {
  return get<ProviderAPIKeyResponse>(`${PROVIDER_PATH}/${id}/api-key`, undefined, signal)
}

export function deleteProvider(id: string) {
  return del(`${PROVIDER_PATH}/${id}`)
}

export function fetchModelLabels(signal?: AbortSignal) {
  return get<ModelLabelOption[]>(`${MODEL_PATH}/label`, undefined, signal)
}

export function fetchModelCatalog(keyword = '', signal?: AbortSignal) {
  // 目录是全量的几千条模型，弹窗内只预载一页，输入关键词后再由服务端检索。
  return get<ModelCatalogResponse>(`${MODEL_PATH}/catalog`, { keyword, page: 1, page_size: 50 }, signal)
}

export function fetchModelCatalogPage(keyword: string, page: number, pageSize: number, signal?: AbortSignal) {
  return get<ModelCatalogResponse>(`${MODEL_PATH}/catalog`, { keyword, page, page_size: pageSize }, signal)
}

export function syncModelCatalog() {
  return post<ModelSyncResponse>(`${MODEL_PATH}/sync`, {})
}

export function createModel(payload: ModelPayload) {
  return post<AIModel>(MODEL_PATH, payload)
}

export function updateModel(id: string, payload: ModelPayload) {
  return put<AIModel>(`${MODEL_PATH}/${id}`, payload)
}

export function deleteModel(id: string) {
  return del(`${MODEL_PATH}/${id}`)
}
