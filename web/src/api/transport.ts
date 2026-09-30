import { desktopApiBase, isDesktop } from '../platform/host'
import { ApiRequestError, transportFailure } from './errors'
import { SUCCESS_CODE } from './codes'

/** 后端统一 envelope；成功码固定 100000，失败帧 HTTP 状态可能为 200。 */
export interface ApiResponse<T> {
  code: number
  data?: T
  message?: string
}

/**
 * 运行时 payload 校验器：网络数据先作为 unknown 接收，由各接口声明必需字段。
 * 校验失败返回带用户可读提示的错误，避免坏响应继续流入 reducer。
 */
export type PayloadGuard<T> = (value: unknown) => { ok: true; value: T } | { ok: false; error: string }

export const guardFailure = (error: string): { ok: false; error: string } => ({ ok: false, error })
export const guardSuccess = <T>(value: T): { ok: true; value: T } => ({ ok: true, value })
export const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value)
export const isString = (value: unknown): value is string => typeof value === 'string'
export const isNumber = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value)
export const isBoolean = (value: unknown): value is boolean => typeof value === 'boolean'
export const isArrayOf = <T>(value: unknown, item: (item: unknown) => item is T): value is T[] => Array.isArray(value) && value.every(item)
export const isOptional = <T>(value: unknown, check: (item: unknown) => item is T): value is T | undefined => value === undefined || check(value)

export const isApiEnvelope = (value: unknown): value is ApiResponse<unknown> => {
  if (!isRecord(value)) return false
  if (!isNumber(value.code)) return false
  return value.message === undefined || typeof value.message === 'string'
}

type QueryValue = string | number | boolean | null | undefined

// Web 构建允许用 VITE_API_BASE_URL 指向外部服务；Desktop 只用宿主注入的本地前缀。
const webApiBaseUrl = (import.meta.env.VITE_API_BASE_URL ?? '/kaguya/api').replace(
  /\/$/,
  '',
)

function resolveApiBase(): string {
  if (!isDesktop()) return webApiBaseUrl
  const resolved = desktopApiBase()
  if (!resolved.ok) throw new ApiRequestError(resolved.error)
  return resolved.prefix
}

/** buildUrl 解析 API base 并拼接 query；非法 Desktop 前缀在请求前抛出。 */
export function buildUrl(path: string, query?: Record<string, QueryValue>) {
  const apiBaseUrl = resolveApiBase()
  const searchParams = new URLSearchParams()

  Object.entries(query ?? {}).forEach(([key, value]) => {
    if (value !== undefined && value !== null && value !== '') {
      searchParams.set(key, String(value))
    }
  })

  const queryString = searchParams.toString()
  return `${apiBaseUrl}${path}${queryString ? `?${queryString}` : ''}`
}

export type HttpMethod = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'

export interface TransportOptions {
  body?: unknown
  query?: Record<string, QueryValue>
  signal?: AbortSignal
  headers?: Record<string, string>
  accept?: string
}

/** request 发起一次 JSON 请求；网络失败归类 network，取消保持 AbortError。 */
export async function request(method: HttpMethod, path: string, options?: TransportOptions): Promise<Response> {
  const url = buildUrl(path, options?.query)
  try {
    return await fetch(url, {
      method,
      headers: {
        Accept: options?.accept ?? 'application/json',
        ...(options?.body === undefined ? {} : { 'Content-Type': 'application/json' }),
        ...options?.headers,
      },
      body: options?.body === undefined ? undefined : JSON.stringify(options.body),
      credentials: 'same-origin',
      signal: options?.signal,
    })
  } catch (error) {
    transportFailure(error, options?.signal)
  }
}

/**
 * parseEnvelope 校验响应 envelope 并按 HTTP 状态/业务码抛出分类错误。
 * !response.ok 时的非 envelope 响应归类 http/protocol，不误报未知业务码。
 */
export async function parseEnvelope(response: Response): Promise<ApiResponse<unknown>> {
  let payload: unknown
  try {
    payload = await response.json()
  } catch {
    throw new ApiRequestError('服务端返回了无法解析的响应', {
      status: response.status,
      kind: response.ok ? 'protocol' : 'http',
    })
  }

  if (!isApiEnvelope(payload)) {
    throw new ApiRequestError('服务端响应格式不正确', {
      status: response.status,
      kind: response.ok ? 'protocol' : 'http',
    })
  }

  if (!response.ok) {
    throw new ApiRequestError(payload.message || `请求失败（${response.status}）`, {
      code: payload.code,
      status: response.status,
      kind: 'business',
    })
  }

  if (payload.code !== SUCCESS_CODE) {
    throw new ApiRequestError(payload.message || '请求处理失败', {
      code: payload.code,
      status: response.status,
      kind: 'business',
    })
  }

  return payload
}

/** requestJson 是 feature API 的公共入口：transport + envelope + guard。 */
export async function requestJson<T>(
  method: HttpMethod,
  path: string,
  options?: TransportOptions,
  guard?: PayloadGuard<T>,
): Promise<T> {
  const response = await request(method, path, options)
  const payload = await parseEnvelope(response)
  if (!guard) return payload.data as T
  const checked = guard(payload.data)
  if (!checked.ok) {
    throw new ApiRequestError(checked.error, { status: response.status, kind: 'protocol' })
  }
  return checked.value
}
