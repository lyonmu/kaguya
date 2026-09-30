/**
 * 统一错误分类。后端业务码只用于 business 场景；网络断开、JSON 坏格式、
 * guard 失败与 HTTP 原始代理响应各有独立 kind，不伪造未知业务码。
 */
export type ApiErrorKind = 'business' | 'network' | 'protocol' | 'http' | 'canceled'

/** 安全上下文：只放关联 ID 等非敏感字段，供 UI 关联会话/审批，不携带原文。 */
export interface ApiErrorContext {
  conversationId?: string
  approvalId?: string
  [key: string]: string | number | boolean | undefined
}

export interface ApiErrorOptions {
  code?: number
  status?: number
  kind?: ApiErrorKind
  context?: ApiErrorContext
}

export class ApiRequestError extends Error {
  readonly kind: ApiErrorKind
  readonly code?: number
  readonly status?: number
  readonly context?: ApiErrorContext

  constructor(message: string, options?: ApiErrorOptions) {
    super(message)
    this.name = 'ApiRequestError'
    this.kind = options?.kind ?? (options?.code === undefined ? 'protocol' : 'business')
    this.code = options?.code
    this.status = options?.status
    this.context = options?.context
  }
}

/** 取消不是错误提示：AbortSignal 触发的 rejection 保持原始 AbortError。 */
export const isAbortError = (error: unknown): boolean =>
  typeof error === 'object' && error !== null && (error as { name?: unknown }).name === 'AbortError'

/** 把任意 thrown 值收敛成 ApiRequestError，保证调用方拿到 kind/code/status。 */
export function asApiRequestError(error: unknown): ApiRequestError {
  if (error instanceof ApiRequestError) return error
  return new ApiRequestError(error instanceof Error ? error.message : '请求失败', { kind: 'protocol' })
}

/** 传输层错误 fallback：abort 保持原样，连接失败归类为 network。 */
export function transportFailure(error: unknown, signal?: AbortSignal): never {
  if (isAbortError(error) || signal?.aborted) throw error
  throw new ApiRequestError('无法连接到服务端，请确认 Kaguya API 已启动', { kind: 'network' })
}
