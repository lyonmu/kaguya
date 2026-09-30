import { requestJson } from './transport'

// 兼容出口：错误类型、envelope 校验与 guard helper 由 transport 统一实现，
// feature API 继续从本模块导入，避免一次迁移所有调用点。
export { ApiRequestError } from './errors'
export { SUCCESS_CODE } from './codes'
export { buildUrl } from './transport'
export {
  guardFailure,
  guardSuccess,
  isApiEnvelope,
  isRecord,
  isString,
  isNumber,
  isBoolean,
  isArrayOf,
  isOptional,
} from './transport'
export type { ApiResponse, PayloadGuard } from './transport'

type QueryValue = string | number | boolean | null | undefined

export function get<T>(
  path: string,
  query?: Record<string, QueryValue>,
  signal?: AbortSignal,
  guard?: import('./transport').PayloadGuard<T>,
): Promise<T> {
  return requestJson<T>('GET', path, { query, signal }, guard)
}

export function post<T>(
  path: string,
  body?: unknown,
  signal?: AbortSignal,
  guard?: import('./transport').PayloadGuard<T>,
): Promise<T> {
  return requestJson<T>('POST', path, { body, signal }, guard)
}

export function put<T>(
  path: string,
  body?: unknown,
  signal?: AbortSignal,
  guard?: import('./transport').PayloadGuard<T>,
): Promise<T> {
  return requestJson<T>('PUT', path, { body, signal }, guard)
}

export function patch<T>(
  path: string,
  body?: unknown,
  signal?: AbortSignal,
  guard?: import('./transport').PayloadGuard<T>,
): Promise<T> {
  return requestJson<T>('PATCH', path, { body, signal }, guard)
}

export function del(
  path: string,
  query?: Record<string, QueryValue>,
  signal?: AbortSignal,
): Promise<void> {
  return requestJson<void>('DELETE', path, { query, signal })
}
