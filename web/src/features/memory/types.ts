// 记忆管理类型与运行时校验；网络数据先作为 unknown 接收再校验。

export type MemoryScope = string

export interface MemoryPageItem {
  id: string
  scope_key: string
  kind: string
  canonical_key: string
  title: string
  summary: string
  status: string
  version: number
  pinned: boolean
  user_locked: boolean
  expires_at?: string
  updated_at: string
}

export interface MemoryPageList {
  total: number
  page: number
  page_size: number
  items: MemoryPageItem[]
}

export interface MemoryEvidence {
  source_id: string
  part_key: string
  quote: string
  relation: string
  source: string
}

export interface MemoryClaim {
  key: string
  statement: string
  basis: string
  evidence: MemoryEvidence[]
}

export interface MemoryPageDetail {
  id: string
  version: number
  scope_key: string
  kind: string
  title: string
  summary: string
  body: string
  status: string
  pinned: boolean
  user_locked: boolean
  aliases: string[]
  claims: MemoryClaim[]
  related_ids: string[]
  source_count: number
  expires_at?: string
  created_at: string
  updated_at: string
}

export interface MemoryRevision {
  version: number
  actor: string
  job_id?: string
  reason: string
  title: string
  summary: string
  body: string
  status: string
  claim_keys: string[]
  created_at: string
}

export interface MemoryJobProgress {
  scanned: number
  created: number
  skipped: number
  limited: boolean
  finished: boolean
}

export interface MemoryJob {
  id: string
  kind: string
  scope_key: string
  conversation_id?: string
  status: string
  attempt: number
  error_code?: string
  error_summary?: string
  proposal?: unknown
  progress?: MemoryJobProgress
  created_at: string
  started_at?: string
  finished_at?: string
  input_tokens: number
  output_tokens: number
  total_tokens: number
  calls: number
}

export interface MemoryJobList {
  total: number
  page: number
  page_size: number
  items: MemoryJob[]
}

export interface MemoryStatus {
  enabled: boolean
  auto_capture: boolean
  context_tokens: number
  policy_epoch: number
  pending_sources: number
  active_pages: number
  index_normalizer: number
  task_model_set: boolean
  blocked_jobs: number
  failed_jobs: number
  review_jobs: number
  memory_input_tokens: number
  memory_output_tokens: number
  memory_total_tokens: number
  memory_calls: number
  memory_usage_known: boolean
}

export interface MemoryRefItem {
  page_id: string
  version: number
  title: string
  status: string
  deleted: boolean
}

export interface MemoryRefs {
  retriever_version: number
  estimated_tokens: number
  items: MemoryRefItem[]
}

export interface MemoryExport {
  filename: string
  markdown: string
}

export interface MemorySourceItem {
  id: string
  kind: string
  scope_key: string
  state: string
  source_key: string
  conversation_id?: string
  turn_id?: string
  document_path?: string
  content_hash?: string
  job_id?: string
  policy_epoch: number
  captured_at: string
}

export interface MemorySourceList {
  total: number
  page: number
  page_size: number
  items: MemorySourceItem[]
}

export interface MemorySourcePart {
  part_key: string
  origin: string
  text: string
  truncated: boolean
}

export interface MemorySourceDetail extends MemorySourceItem {
  available: boolean
  unavailable_reason?: string
  turn_status?: string
  finish_reason?: string
  parts: MemorySourcePart[]
}

export interface MemoryBackfillResult {
  job_id: string
  scope_key: string
  status: string
  scanned: number
  created: number
  skipped: number
  max_sources: number
  limited: boolean
  finished: boolean
  error_code?: string
}

export interface MemoryImportResult {
  source_id: string
  state: string
  path: string
  size: number
  deduplicated: boolean
}

export interface MemoryDiffSide {
  version: number
  actor: string
  reason: string
  created_at: string
  title: string
  summary: string
  body: string
  kind: string
  status: string
  aliases: string[]
}

export interface MemoryFieldChange {
  field: string
  from: string
  to: string
}

export interface MemoryClaimChange {
  key: string
  change: string
  from?: string
  to?: string
  from_basis?: string
  to_basis?: string
}

export interface MemoryEvidenceChange {
  claim_key: string
  change: string
  source_id: string
  part_key: string
  quote?: string
  relation?: string
  basis?: string
}

export interface MemoryDiff {
  page_id: string
  from: MemoryDiffSide
  to: MemoryDiffSide
  content_changes: MemoryFieldChange[]
  metadata_changes: MemoryFieldChange[]
  claim_changes: MemoryClaimChange[]
  evidence_changes: MemoryEvidenceChange[]
}

export interface MemorySourceRef {
  conversation_id: string
  turn_id: string
  part_key: string
  quote: string
}

export interface MemoryPagePayload {
  scope_key: string
  kind: string
  canonical_key?: string
  title: string
  summary?: string
  body: string
  aliases?: string[]
  expires_at?: string
  pinned?: boolean
  user_locked?: boolean
  related_ids?: string[]
  reason?: string
  source?: MemorySourceRef
}

export interface MemoryPagePatch extends Partial<MemoryPagePayload> {
  expected_version: number
  clear_expires_at?: boolean
  scope_key?: string
}

export const MEMORY_KINDS = ['preference', 'fact', 'decision', 'procedure', 'lesson'] as const
export const MEMORY_STATUSES = ['proposed', 'active', 'conflicted', 'stale', 'archived', 'deleted'] as const

export const basisLabel: Record<string, string> = {
  user_statement: '用户明确决定',
  tool_observation: '工具观察',
  document_statement: '资料陈述',
  synthesis: '综合推断（待确认）',
}

export const statusLabel: Record<string, string> = {
  proposed: '待审',
  active: '有效',
  conflicted: '冲突',
  stale: '待核验',
  archived: '已停用',
  deleted: '已删除',
}
