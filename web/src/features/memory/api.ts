import { del, get, patch, post, guardFailure, guardSuccess, isArrayOf, isBoolean, isNumber, isRecord, isString, type PayloadGuard } from '../../api/http'
import type {
  MemoryBackfillResult, MemoryClaim, MemoryDiff, MemoryEvidence, MemoryExport, MemoryImportResult, MemoryJob, MemoryJobList,
  MemoryPageDetail, MemoryPageItem, MemoryPageList, MemoryPagePatch, MemoryPagePayload, MemoryRevision,
  MemorySourceDetail, MemorySourceList, MemoryStatus,
} from './types'

const isMemoryPageItem = (value: unknown): value is MemoryPageItem =>
  isRecord(value) && isString(value.id) && isString(value.scope_key) && isString(value.title) &&
  isString(value.status) && isNumber(value.version)

const isEvidence = (value: unknown): value is MemoryEvidence =>
  isRecord(value) && isString(value.source_id) && isString(value.part_key) && isString(value.quote) &&
  isString(value.source) && isString(value.relation)

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
  if (!isRecord(value)) return guardFailure('记忆详情响应格式异常')
  // Older servers serialize empty Go slices as null. Normalize only empty
  // collections; reject malformed entries before they reach React rendering.
  const normalized = {
    ...value,
    aliases: value.aliases ?? [],
    related_ids: value.related_ids ?? [],
    claims: Array.isArray(value.claims) ? value.claims.map(claim =>
      isRecord(claim) ? { ...claim, evidence: claim.evidence ?? [] } : claim) : value.claims ?? [],
  }
  if (!isMemoryPageItem(value) || !isString(value.kind) || !isString(value.summary) || !isString(value.body) ||
    !isNumber(value.source_count) || !isBoolean(value.pinned) || !isBoolean(value.user_locked) ||
    !isArrayOf(normalized.aliases, isString) || !isArrayOf(normalized.related_ids, isString) ||
    !isArrayOf(normalized.claims, isClaim)) {
    return guardFailure('记忆详情响应格式异常')
  }
  return guardSuccess(normalized as unknown as MemoryPageDetail)
}

const guardRevisions: PayloadGuard<MemoryRevision[]> = value => {
  const check = (item: unknown): item is MemoryRevision =>
    isRecord(item) && isNumber(item.version) && isString(item.actor) && isString(item.body) &&
    isString(item.title) && isString(item.summary) && isString(item.reason) && isString(item.created_at)
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

const isSourcePart = (value: unknown): value is MemorySourceDetail['parts'][number] =>
  isRecord(value) && isString(value.part_key) && isString(value.text)

const isSourceItem = (value: unknown): value is MemorySourceDetail =>
  isRecord(value) && isString(value.id) && isString(value.kind) && isString(value.state) &&
  isBoolean(value.available) && isArrayOf(value.parts, isSourcePart)

const guardSourceList: PayloadGuard<MemorySourceList> = value => {
  const check = (item: unknown): item is MemorySourceList['items'][number] =>
    isRecord(item) && isString(item.id) && isString(item.kind) && isString(item.state)
  if (!isRecord(value) || !isNumber(value.total) || !isArrayOf(value.items, check)) {
    return guardFailure('记忆来源响应格式异常')
  }
  return guardSuccess({ total: value.total, page: Number(value.page ?? 1), page_size: Number(value.page_size ?? 20), items: value.items })
}

const guardSourceDetail: PayloadGuard<MemorySourceDetail> = value => {
  if (!isSourceItem(value)) return guardFailure('记忆来源详情响应格式异常')
  return guardSuccess(value)
}

const guardBackfill: PayloadGuard<MemoryBackfillResult> = value => {
  if (!isRecord(value) || !isString(value.job_id) || !isString(value.status) || !isNumber(value.created)) {
    return guardFailure('历史回填响应格式异常')
  }
  return guardSuccess(value as unknown as MemoryBackfillResult)
}

const guardImport: PayloadGuard<MemoryImportResult> = value => {
  if (!isRecord(value) || !isString(value.source_id) || !isString(value.state) || !isBoolean(value.deduplicated)) {
    return guardFailure('资料导入响应格式异常')
  }
  return guardSuccess(value as unknown as MemoryImportResult)
}

const guardDiff: PayloadGuard<MemoryDiff> = value => {
  if (!isRecord(value) || !isRecord(value.from) || !isRecord(value.to) ||
    !isArrayOf(value.content_changes, isRecord) || !isArrayOf(value.metadata_changes, isRecord) ||
    !isArrayOf(value.evidence_changes, isRecord) || !isArrayOf(value.claim_changes, isRecord)) {
    return guardFailure('版本对比响应格式异常')
  }
  return guardSuccess(value as unknown as MemoryDiff)
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

export function fetchMemoryJobs(status?: string, signal?: AbortSignal, page = 1) {
  return get<MemoryJobList>('/v1/memory/jobs', { status, page }, signal, guardJobs)
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

export function startMemoryBackfill(payload: { scope_key: string; conversation_id?: string; max_sources?: number; after?: string; before?: string }) {
  return post<MemoryBackfillResult>('/v1/memory/backfill', payload, undefined, guardBackfill)
}

export function importMemoryDocument(payload: { scope_key: string; path: string }) {
  return post<MemoryImportResult>('/v1/memory/import', payload, undefined, guardImport)
}

export interface MemorySourceQuery {
  scope_key?: string
  kind?: string
  state?: string
  page?: number
  page_size?: number
}

export function fetchMemorySources(query: MemorySourceQuery = {}, signal?: AbortSignal) {
  return get<MemorySourceList>('/v1/memory/sources', { ...query }, signal, guardSourceList)
}

export function fetchMemorySource(id: string, signal?: AbortSignal) {
  return get<MemorySourceDetail>(`/v1/memory/sources/${encodeURIComponent(id)}`, undefined, signal, guardSourceDetail)
}

export function fetchMemoryPageDiff(id: string, from: number, to?: number, signal?: AbortSignal) {
  return get<MemoryDiff>(`/v1/memory/pages/${encodeURIComponent(id)}/diff`, { from, to }, signal, guardDiff)
}
