import { guardFailure, guardSuccess, isArrayOf, isBoolean, isOptional, isRecord, isString, type PayloadGuard } from '../../api/transport'
import type { Reference, ReferenceContinuation, ReferenceFile, ReferenceFileStatus, ReferencePartialReason, ReferenceRange, ReferenceSource, ReferenceSummary } from './types'

const count = (value: unknown): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
const positive = (value: unknown): value is number => count(value) && value > 0
const path = (value: unknown): value is string => isString(value) && value.length > 0 && value.length <= 4096 && !value.includes('\0')
const partial = (value: unknown): value is ReferencePartialReason => value === 'deadline' || value === 'metadata_budget' || value === 'context_budget'
const range = (value: unknown): value is ReferenceRange => isRecord(value) && positive(value.start) && positive(value.end) && value.start <= value.end
const strings = (value: unknown): value is string[] => isArrayOf(value, isString)
const ranges = (value: unknown): value is ReferenceRange[] => isArrayOf(value, range)
const statuses: ReferenceFileStatus[] = ['included', 'not_included', 'empty', 'binary', 'too_large', 'oversized_line', 'read_failed']

function file(value: unknown): ReferenceFile | undefined {
  if (!isRecord(value) || !path(value.path) || !isOptional(value.resolved_path, path) || !isBoolean(value.included)) return
  if (!isString(value.status) || !statuses.includes(value.status as ReferenceFileStatus)) return
  const lines = value.ranges ?? []
  if (!ranges(lines) || !isOptional(value.hash, isString) || !isOptional(value.next_line, positive)) return
  const nextLine = value.next_line
  if (value.included) {
    if (value.status !== 'included' || !isString(value.hash) || !/^[a-f0-9]{64}$/.test(value.hash) || !lines.length) return
    if (nextLine !== undefined && lines.some(item => nextLine <= item.end)) return
  } else if (value.status === 'included' || lines.length || value.hash || value.next_line !== undefined) return
  return { path: value.path, resolved_path: value.resolved_path, included: value.included, status: value.status as ReferenceFileStatus, hash: value.hash, ranges: lines, next_line: value.next_line }
}

function source(value: unknown, legacy: boolean): ReferenceSource | undefined {
  if (!isRecord(value) || (value.type !== 'file' && value.type !== 'directory') || !path(value.path)) return
  const lines = value.ranges ?? [], warnings = value.warnings ?? [], rawFiles = value.files ?? []
  if (!ranges(lines) || !strings(warnings) || !Array.isArray(rawFiles)) return
  if (!isOptional(value.included, isBoolean) || !isOptional(value.hash, (hash): hash is string => isString(hash) && (hash === '' || /^[a-f0-9]{16}$/.test(hash)))) return
  const files: ReferenceFile[] = []
  for (const raw of rawFiles) { const next = file(raw); if (!next) return; files.push(next) }
  if (value.included === true && !value.hash) return
  if (value.included !== true && (lines.length || value.hash || files.some(item => item.included))) {
    // 已提交旧版本没有 included/files 字段；保留其记录，不推导实际读取阶段。
    if (!legacy || value.included !== undefined || files.length) return
  }
  return { type: value.type, path: value.path, included: value.included ?? (legacy ? undefined : false), hash: value.hash, ranges: lines, warnings, files }
}

function continuation(value: unknown): ReferenceContinuation | undefined {
  if (!isRecord(value) || !path(value.root) || !partial(value.reason) || !isOptional(value.last_stable_path, path) || !isOptional(value.known_omitted_files, count)) return
  const subtrees = value.unscanned_subtrees ?? []
  if (!isArrayOf(subtrees, path)) return
  return { root: value.root, reason: value.reason, last_stable_path: value.last_stable_path, unscanned_subtrees: subtrees, known_omitted_files: value.known_omitted_files ?? 0 }
}

/** HTTP/history/SSE 共用边界；只归一化可空数组，不把未知容量或状态转成成功。 */
export const referenceSummaryGuard: PayloadGuard<ReferenceSummary> = value => {
  const failure = () => guardFailure('引用资料格式不正确')
  if (!isRecord(value)) return failure()
  if (!isOptional(value.preparation_state, (state): state is 'requested' | 'resolved' => state === 'requested' || state === 'resolved')) return failure()
  if (!isOptional(value.scan_incomplete, isBoolean) || !isOptional(value.budget_known, isBoolean) || !isOptional(value.partial_reason, partial)) return failure()
  if (!['estimated_tokens', 'inline_bytes'].every(field => isOptional(value[field], count))) return failure()
  const rawSources = value.sources ?? [], warnings = value.warnings ?? [], rawContinuation = value.continuation ?? []
  if (!Array.isArray(rawSources) || !strings(warnings) || !Array.isArray(rawContinuation)) return failure()
  const sources: ReferenceSource[] = [], next: ReferenceContinuation[] = []
  for (const raw of rawSources) { const item = source(raw, value.preparation_state === undefined); if (!item) return failure(); sources.push(item) }
  for (const raw of rawContinuation) { const item = continuation(raw); if (!item) return failure(); next.push(item) }
  if (value.preparation_state === 'requested' && (sources.some(item => item.included || item.hash || item.ranges.length || item.files.length) || value.scan_incomplete || value.budget_known !== undefined || value.partial_reason || next.length || value.inline_bytes || value.estimated_tokens)) return failure()
  return guardSuccess({
    preparation_state: value.preparation_state, sources, warnings, scan_incomplete: value.scan_incomplete === true,
    budget_known: value.budget_known, partial_reason: value.partial_reason, continuation: next,
    estimated_tokens: (value.estimated_tokens as number | undefined) ?? 0, inline_bytes: (value.inline_bytes as number | undefined) ?? 0,
  })
}

/** 仅恢复选择，永不把摘要、hash 或旧正文当成执行权限。 */
export function referenceSelections(summary: ReferenceSummary | undefined): Reference[] {
  const seen = new Set<string>()
  return (summary?.sources ?? []).flatMap(({ type, path }) => {
    const key = type + '\0' + path
    if (seen.has(key)) return []
    seen.add(key)
    return [{ type, path }]
  })
}
