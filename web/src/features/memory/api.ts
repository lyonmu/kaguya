import { del, get, patch, post, guardFailure, guardSuccess, isArrayOf, isBoolean, isNumber, isRecord, isString, type PayloadGuard } from '../../api/http'
import type {
  MemoryClaim, MemoryEvidence, MemoryExport, MemoryJob, MemoryJobList, MemoryPageDetail,
  MemoryPageItem, MemoryPageList, MemoryPagePatch, MemoryPagePayload, MemoryRevision, MemoryStatus,
} from './types'

const isMemoryPageItem = (value: unknown): value is MemoryPageItem =>
  isRecord(value) && isString(value.id) && isString(value.scope_key) && isString(value.title) &&
  isString(value.status) && isNumber(value.version)

const isEvidence = (value: unknown): value is MemoryEvidence =>
  isRecord(value) && isString(value.source_id) && isString(value.part_key) && isString(value.quote)

const isClaim = (value: unknown): value is MemoryClaim =>
  isRecord(value) && isString(value.key) && isString(value.statement) && isString(value.basis) &&
  isArrayOf(value.evidence, isEvidence)

const guardPageList: PayloadGuard<MemoryPageList> = value => {
  if (!isRecord(value) || !isNumber(value.total) || !isArrayOf(value.items, isMemoryPageItem)) {
    return guardFailure('记忆列表响应格式异常')
  }
  return guardSuccess({ total: value.total, page: Number(value.page ?? 1), page_size: Number(value.page_size ?? 20), items: value.items })
}

const guardPageDetail: PayloadGuard<MemoryPageDetail> = value => {
  if (!isRecord(value) || !isString(value.id) || !isNumber(value.version) || !isString(value.body) ||
    !isArrayOf(value.aliases, isString) || !isArrayOf(value.claims, isClaim)) {
    return guardFailure('记忆详情响应格式异常')
  }
  return guardSuccess(value as unknown as MemoryPageDetail)
}

const guardRevisions: PayloadGuard<MemoryRevision[]> = value => {
  const check = (item: unknown): item is MemoryRevision =>
    isRecord(item) && isNumber(item.version) && isString(item.actor) && isString(item.body)
  if (!isArrayOf(value, check)) return guardFailure('记忆修订响应格式异常')
  return guardSuccess(value)
}

const guardJobs: PayloadGuard<MemoryJobList> = value => {
  const check = (item: unknown): item is MemoryJob =>
    isRecord(item) && isString(item.id) && isString(item.status) && isNumber(item.total_tokens)
  if (!isRecord(value) || !isNumber(value.total) || !isArrayOf(value.items, check)) {
    return guardFailure('记忆任务响应格式异常')
  }
  return guardSuccess({ total: value.total, page: Number(value.page ?? 1), page_size: Number(value.page_size ?? 20), items: value.items })
}

const guardStatus: PayloadGuard<MemoryStatus> = value => {
  if (!isRecord(value) || !isBoolean(value.enabled) || !isNumber(value.pending_sources) ||
    !isNumber(value.memory_total_tokens)) {
    return guardFailure('记忆状态响应格式异常')
  }
  return guardSuccess(value as unknown as MemoryStatus)
}

const guardExport: PayloadGuard<MemoryExport> = value => {
  if (!isRecord(value) || !isString(value.markdown)) return guardFailure('记忆导出响应格式异常')
  return guardSuccess({ filename: String(value.filename ?? 'memory.md'), markdown: value.markdown })
}

export interface MemoryPageQuery {
  scope_key?: string
  status?: string
  keyword?: string
  page?: number
  page_size?: number
}

export function fetchMemoryPages(query: MemoryPageQuery, signal?: AbortSignal) {
  return get<MemoryPageList>('/v1/memory/pages', { ...query }, signal, guardPageList)
}

export function fetchMemoryPage(id: string, version?: number, signal?: AbortSignal) {
  return get<MemoryPageDetail>(`/v1/memory/pages/${encodeURIComponent(id)}`, { version }, signal, guardPageDetail)
}

export function createMemoryPage(payload: MemoryPagePayload) {
  return post<MemoryPageDetail>('/v1/memory/pages', payload, undefined, guardPageDetail)
}

export function updateMemoryPage(id: string, payload: MemoryPagePatch) {
  return patch<MemoryPageDetail>(`/v1/memory/pages/${encodeURIComponent(id)}`, payload, guardPageDetail)
}

export function deleteMemoryPage(id: string, mode: 'disable' | 'forget') {
  return del(`/v1/memory/pages/${encodeURIComponent(id)}`, { mode })
}

export function fetchMemoryRevisions(id: string, signal?: AbortSignal) {
  return get<MemoryRevision[]>(`/v1/memory/pages/${encodeURIComponent(id)}/revisions`, undefined, signal, guardRevisions)
}

export function restoreMemoryRevision(id: string, version: number) {
  return post<MemoryPageDetail>(`/v1/memory/pages/${encodeURIComponent(id)}/restore`, { version }, undefined, guardPageDetail)
}

export function fetchMemoryJobs(status?: string, signal?: AbortSignal) {
  return get<MemoryJobList>('/v1/memory/jobs', { status }, signal, guardJobs)
}

export function retryMemoryJob(id: string) {
  return post(`/v1/memory/jobs/${encodeURIComponent(id)}/retry`)
}

export function approveMemoryJob(id: string) {
  return post(`/v1/memory/jobs/${encodeURIComponent(id)}/approve`)
}

export function rejectMemoryJob(id: string) {
  return post(`/v1/memory/jobs/${encodeURIComponent(id)}/reject`)
}

export function compileMemory(scopeKey: string) {
  return post('/v1/memory/compile', { scope_key: scopeKey })
}

export function fetchMemoryStatus(signal?: AbortSignal) {
  return get<MemoryStatus>('/v1/memory/status', undefined, signal, guardStatus)
}

export function exportMemory(scopeKeys: string[]) {
  return post<MemoryExport>('/v1/memory/export', { scope_keys: scopeKeys }, undefined, guardExport)
}
