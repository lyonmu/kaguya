import type { ApprovalDecision, Approval, Block, ChatFrame, Conversation, ConversationContext, ConversationPage, ConversationTitle, PermissionMode, TitleEvent, TitleJobStatus, TitleSnapshot, TitleSnapshots, Turn, TurnPage, TurnPreparationUsage, Usage } from './types'
import { guardFailure, guardSuccess, isArrayOf, isBoolean, isNumber, isOptional, isRecord, isString, type PayloadGuard } from '../../api/http'

import { referenceSummaryGuard } from './referenceSummary'

// 本模块集中声明网络边界上的必需字段；允许后端追加未知字段，
// 只拒绝缺失、类型错误或明显破坏 reducer 的响应。

// 字段缺失时按 0 处理；出现时必须类型正确，避免 NaN 进入展示层。
const isUsage = (value: unknown, required: boolean): value is Usage => {
  if (!isRecord(value)) return false
  const numbers = ['input_tokens', 'output_tokens', 'total_tokens', 'cached_tokens', 'cache_creation_tokens', 'reasoning_tokens']
  return numbers.some(field => !isOptional(value[field], isNumber)) ? false : required ? isNumber(value.total_tokens) : true
}

const isAuditCount = (value: unknown): value is number => isNumber(value) && Number.isSafeInteger(value) && value >= 0
const isTitleID = (value: unknown): value is string => isString(value) && !!value.trim() && !value.includes('\0') && [...value].length <= 64
const isTitleJobStatus = (value: unknown): value is TitleJobStatus => isString(value) && ['pending', 'running', 'succeeded', 'failed', 'canceled'].includes(value)
const isNullableJobStatus = (value: unknown): value is TitleJobStatus | null => value === null || isTitleJobStatus(value)
const isPreparationUsage = (value: unknown): value is TurnPreparationUsage => {
  if (!isRecord(value) || value.version !== 1 || !isAuditCount(value.summary_calls) || !isAuditCount(value.reported_calls) || !isAuditCount(value.unreported_calls)) return false
  if (value.summary_calls !== value.reported_calls + value.unreported_calls || !isRecord(value.usage)) return false
  const usage = value.usage
  return ['input_tokens', 'output_tokens', 'total_tokens', 'cache_hit_tokens', 'cache_creation_tokens', 'reasoning_tokens'].every(field => isAuditCount(usage[field]))
}

const blockTypes = ['text', 'reasoning', 'tool_call', 'tool_result']
const blockPhases = ['start', 'delta', 'block_end']

export const isBlock = (value: unknown): value is Block =>
  isRecord(value) &&
  isString(value.type) &&
  blockTypes.includes(value.type) &&
  isOptional(value.phase, (phase): phase is NonNullable<Block['phase']> => isString(phase) && blockPhases.includes(phase))

const isTurn = (value: unknown): value is Turn =>
  isRecord(value) &&
  isNumber(value.turn_index) &&
  isString(value.user_content) &&
  isString(value.started_at) &&
  isArrayOf(value.blocks, isBlock) &&
  isOptional(value.approvals, (items): items is Approval[] => isArrayOf(items, isApproval)) &&
  isOptional(value.preparation_usage, isPreparationUsage) &&
  isOptional(value.usage, (item): item is Usage => isUsage(item, false))

const isConversation = (value: unknown): value is Conversation =>
  isRecord(value) &&
  isString(value.id) &&
  isString(value.title) &&
  isOptional(value.turn_count, isNumber) &&
  isOptional(value.last_model_id, isString) &&
  isOptional(value.title_revision, isAuditCount) &&
  isOptional(value.permission_mode, isString) &&
  isOptional(value.title_job_status, isNullableJobStatus) &&
  isOptional(value.title_job_revision, isAuditCount) &&
  (value.title_job_status != null || value.title_job_revision === undefined || value.title_job_revision === 0) &&
  isOptional(value.instructions_format, isString) &&
  isOptional(value.instructions_legacy, isBoolean) &&
  isOptional(value.usage, (item): item is Usage => isUsage(item, false))

export const conversationGuard: PayloadGuard<Conversation> = value =>
  isConversation(value) ? guardSuccess(value) : guardFailure('会话数据格式不正确')

// items 是唯一必需字段；total/page 可选，缺失时由调用方按实际长度处理。
export const conversationPageGuard: PayloadGuard<ConversationPage> = value =>
  isRecord(value) && isArrayOf(value.items, isConversation)
    ? guardSuccess(value as unknown as ConversationPage)
    : guardFailure('会话列表格式不正确')

export const turnPageGuard: PayloadGuard<TurnPage> = value => {
  if (!isRecord(value) || !Array.isArray(value.items) || !isNumber(value.page) || !isNumber(value.total_pages)) return guardFailure('历史轮次格式不正确')
  const items: Turn[] = []
  for (const raw of value.items) {
    const checked = turnGuard(raw)
    if (!checked.ok) return checked
    items.push(checked.value)
  }
  return guardSuccess({ ...value, items } as unknown as TurnPage)
}

export const turnGuard: PayloadGuard<Turn> = value => {
  if (!isTurn(value)) return guardFailure('轮次数据格式不正确')
  if (value.reference_summary == null) return guardSuccess({ ...value, reference_summary: undefined })
  const summary = referenceSummaryGuard(value.reference_summary)
  return summary.ok ? guardSuccess({ ...value, reference_summary: summary.value }) : summary
}

export const blockGuard: PayloadGuard<Block> = value =>
  isBlock(value) ? guardSuccess(value) : guardFailure('轮次内容格式不正确')

export const conversationTitleGuard: PayloadGuard<ConversationTitle> = value => {
  if (!isRecord(value) || !isTitleID(value.id) || !isString(value.title)) return guardFailure('标题数据格式不正确')
  if (!isOptional(value.revision, isAuditCount) || !isOptional(value.status, isString)) return guardFailure('标题数据格式不正确')
  return guardSuccess({ id: value.id, title: value.title, revision: value.revision, status: value.status })
}

const isApproval = (value: unknown): value is Approval => {
  if (!isRecord(value)) return false
  if (!isString(value.approval_id) || !isString(value.tool_call_id) || !isString(value.tool_name)) return false
  if (!isString(value.effect) || !isString(value.mode) || !isString(value.status)) return false
  if (!['read_only', 'mutating', 'unknown'].includes(value.effect) || !['full_approval', 'auto_approval', 'full_access'].includes(value.mode)) return false
  if (!['pending', 'approved', 'rejected', 'expired', 'canceled', 'consumed'].includes(value.status)) return false
  if (!isOptional(value.input, isString) || !isOptional(value.tool_identity, isString) || !isOptional(value.expires_at, isNumber)) return false
  if (!isOptional(value.execution_state, (state): state is NonNullable<Approval['execution_state']> => isString(state) && ['not_started', 'unknown', 'succeeded', 'failed', 'canceled'].includes(state))) return false
  if (!isOptional(value.execution_finished_at, isNumber) || !isOptional(value.execution_reason_code, isString)) return false
  return true
}

export const approvalGuard: PayloadGuard<Approval> = value =>
  isApproval(value) ? guardSuccess(value) : guardFailure('审批数据格式不正确')

export const approvalListGuard: PayloadGuard<Approval[]> = value =>
  isArrayOf(value, isApproval) ? guardSuccess(value) : guardFailure('审批列表格式不正确')

export const approvalDecisionGuard: PayloadGuard<ApprovalDecision> = value =>
  isRecord(value) && isString(value.approval_id) && isString(value.status) &&
  ['pending', 'approved', 'rejected', 'expired', 'canceled', 'consumed'].includes(value.status) &&
  isOptional(value.decision, (decision): decision is string => decision === 'approve' || decision === 'reject')
    ? guardSuccess(value as unknown as ApprovalDecision)
    : guardFailure('审批裁决格式不正确')

// 只有 conversation_id 是必需的；可选字段出现时必须类型正确，缺失时展示层按未知/0 处理。
export const conversationContextGuard: PayloadGuard<ConversationContext> = value => {
  if (!isRecord(value) || !isString(value.conversation_id)) {
    return guardFailure('上下文用量格式不正确')
  }
  for (const field of ['model_id', 'model_name'] as const) {
    if (!isOptional(value[field], isString)) return guardFailure('上下文用量格式不正确')
  }
  for (const field of ['turn_index', 'context_window', 'effective_window'] as const) {
    if (!isOptional(value[field], isNumber)) return guardFailure('上下文用量格式不正确')
  }
  return guardSuccess(value as unknown as ConversationContext)
}

const frameFlags = ['start', 'delta', 'done', 'error', 'approval']

// sseFrameGuard 校验下行帧的关键字段；usage/content/block/approval 可选，但出现时必须结构正确。
// 会话尚未建立时的错误帧允许空 id，与 API 层的失败响应一致。
export const sseFrameGuard: PayloadGuard<ChatFrame> = value => {
  if (!isRecord(value) || !isRecord(value.chat)) return guardFailure('SSE 事件格式不正确')
  const chat = value.chat
  if (!isString(chat.flag) || !frameFlags.includes(chat.flag)) return guardFailure('SSE 事件格式不正确')
  if (!isString(chat.id)) return guardFailure('SSE 事件格式不正确')
  if (chat.content !== undefined && !isString(chat.content)) return guardFailure('SSE 事件格式不正确')
  if (chat.block !== undefined && !isBlock(chat.block)) return guardFailure('SSE 事件格式不正确')
  let frame = value as unknown as ChatFrame
  if (value.reference_summary != null) {
    const summary = referenceSummaryGuard(value.reference_summary)
    if (!summary.ok) return summary
    frame = { ...frame, reference_summary: summary.value }
  } else { frame = { ...frame, reference_summary: undefined } }
  if (chat.flag === 'approval') {
    const checked = approvalGuard(value.approval)
    if (!checked.ok) return guardFailure('SSE 事件格式不正确')
    return guardSuccess(frame)
  }
  if (!isOptional(value.usage, (item): item is Usage => isUsage(item, true))) return guardFailure('SSE 事件格式不正确')
  return guardSuccess(frame)
}

// 会话权限模式必须属于后端稳定枚举；未知值拒绝发出。
const permissionModes: PermissionMode[] = ['full_approval', 'auto_approval', 'full_access']

export const permissionModeGuard = (value: unknown): PermissionMode | undefined =>
  isString(value) && (permissionModes as string[]).includes(value) ? (value as PermissionMode) : undefined

// 标题事件只接受已知类型与必填 ID/revision；title 缺失表示任务终态无新标题。
export const titleEventGuard: PayloadGuard<TitleEvent> = value => {
  if (!isRecord(value) || !isTitleID(value.conversation_id) || !isOptional(value.title, isString) || !isOptional(value.status, isString)) return guardFailure('标题事件格式不正确')
  if (!isOptional(value.revision, isAuditCount) || !isOptional(value.job_revision, isAuditCount) || !isOptional(value.job_status, isTitleJobStatus)) return guardFailure('标题事件格式不正确')
  if (value.type === 'conversation.title.updated' && isAuditCount(value.revision)) return guardSuccess(value as unknown as TitleEvent)
  if (value.type === 'conversation.title.job.updated' && isAuditCount(value.job_revision) && isTitleJobStatus(value.job_status)) return guardSuccess(value as unknown as TitleEvent)
  return guardFailure('标题事件格式不正确')
}

const isTitleSnapshot = (value: unknown): value is TitleSnapshot => isRecord(value) && isTitleID(value.id) && isString(value.title) &&
  isAuditCount(value.title_revision) && isNullableJobStatus(value.job_status) && isAuditCount(value.job_revision) && (value.job_status !== null || value.job_revision === 0)

/** 请求集合必须被完整且唯一覆盖，禁止跨ID更新或缺失集合与实体重叠。 */
export function titleSnapshotsGuard(ids: readonly string[]): PayloadGuard<TitleSnapshots> {
  return value => {
    if (!isRecord(value) || !isArrayOf(value.items, isTitleSnapshot) || !isArrayOf(value.missing_ids, isTitleID)) return guardFailure('标题快照格式不正确')
    const remaining = new Set(ids)
    for (const id of [...value.items.map(item => item.id), ...value.missing_ids]) {
      if (!remaining.delete(id)) return guardFailure('标题快照集合不正确')
    }
    return remaining.size === 0 ? guardSuccess({ items: value.items, missing_ids: value.missing_ids }) : guardFailure('标题快照集合不完整')
  }
}
