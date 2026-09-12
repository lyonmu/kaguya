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

export const SUCCESS_CODE = 100000
const apiBaseUrl = (import.meta.env.VITE_API_BASE_URL ?? '/kaguya/api').replace(
  /\/$/,
  '',
)

export class ApiRequestError extends Error {
  code?: number
  status?: number

  constructor(message: string, options?: { code?: number; status?: number }) {
    super(message)
    this.name = 'ApiRequestError'
    this.code = options?.code
    this.status = options?.status
  }
}

export function buildUrl(path: string, query?: Record<string, QueryValue>) {
  const searchParams = new URLSearchParams()

  Object.entries(query ?? {}).forEach(([key, value]) => {
    if (value !== undefined && value !== null && value !== '') {
      searchParams.set(key, String(value))
    }
  })

  const queryString = searchParams.toString()
  return `${apiBaseUrl}${path}${queryString ? `?${queryString}` : ''}`
}

async function request<T>(
  method: 'GET' | 'POST' | 'PUT' | 'DELETE',
  path: string,
  options?: {
    body?: unknown
    query?: Record<string, QueryValue>
    signal?: AbortSignal
  },
  guard?: PayloadGuard<T>,
): Promise<T> {
  let response: Response

  try {
    response = await fetch(buildUrl(path, options?.query), {
      method,
      headers: {
        Accept: 'application/json',
        ...(options?.body === undefined ? {} : { 'Content-Type': 'application/json' }),
      },
      body: options?.body === undefined ? undefined : JSON.stringify(options.body),
      credentials: 'same-origin',
      signal: options?.signal,
    })
  } catch (requestError) {
    const signal = options?.signal
    if (signal?.aborted) {
      throw requestError
    }

    throw new ApiRequestError('无法连接到服务端，请确认 Kaguya API 已启动')
  }

  let payload: unknown

  try {
    payload = await response.json()
  } catch {
    throw new ApiRequestError('服务端返回了无法解析的响应', {
      status: response.status,
    })
  }

  if (!isApiEnvelope(payload)) {
    throw new ApiRequestError('服务端响应格式不正确', {
      status: response.status,
    })
  }

  if (!response.ok) {
    throw new ApiRequestError(payload.message || `请求失败（${response.status}）`, {
      code: payload.code,
      status: response.status,
    })
  }

  if (payload.code !== SUCCESS_CODE) {
    throw new ApiRequestError(payload.message || '请求处理失败', {
      code: payload.code,
      status: response.status,
    })
  }

  if (!guard) {
    return payload.data as T
  }
  const checked = guard(payload.data)
  if (!checked.ok) {
    throw new ApiRequestError(checked.error, { status: response.status })
  }
  return checked.value
}

export function get<T>(
  path: string,
  query?: Record<string, QueryValue>,
  signal?: AbortSignal,
  guard?: PayloadGuard<T>,
): Promise<T> {
  return request<T>('GET', path, { query, signal }, guard)
}

export function post<T>(
  path: string,
  body?: unknown,
  signal?: AbortSignal,
  guard?: PayloadGuard<T>,
): Promise<T> {
  return request<T>('POST', path, { body, signal }, guard)
}

export function put<T>(path: string, body?: unknown, guard?: PayloadGuard<T>): Promise<T> {
  return request<T>('PUT', path, { body }, guard)
}

export function del(path: string): Promise<void> {
  return request<void>('DELETE', path)
}
