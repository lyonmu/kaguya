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
