import type { Approval, Conversation, PermissionMode, Reference, Turn } from './types'
import { mergeTitle, titleFromConversation } from './titleMerge'

/** 单会话的本地状态；stream 与 session 绑定，导航只改变 selected。 */
export interface Session {
  key: string
  id: string
  conversation?: Conversation
  turns: Turn[]
  page: number
  totalPages: number
  initialEnd: boolean
  loading: boolean
  streaming: boolean
  error: string
  stream?: AbortController
  request?: AbortController
  completion?: AbortController
  title?: AbortController
  confirm?: AbortController
  // opVersion 标识会话当前的持久化操作（发送/切页/重载/删除）；延迟确认只在
  // 版本未变化且会话仍注册时应用，避免覆盖更新的轮次或分页。
  opVersion: number
  projectId?: string
  draft: string
  // 只由用户编辑推进；失败恢复不能覆盖之后的草稿或引用选择。
  draftVersion: number
  references: Reference[]
  // approvals 按 approval_id 保存本轮审批状态；轮次结束或新发送时清空。
  approvals: Map<string, Approval>
  permissionMode: PermissionMode
  modelId: string
  // modelSeeded 标记已从会话详情沿用最近一轮模型；之后的重载与手动选择不再覆盖。
  modelSeeded?: boolean
}

let nextKey = 0

export function createSession(id = ''): Session {
  return { key: `session-${++nextKey}`, id, draft: '', draftVersion: 0, references: [], approvals: new Map(), permissionMode: 'full_access', modelId: '', turns: [], page: 1, totalPages: 0, initialEnd: true, loading: false, streaming: false, error: '', opVersion: 0 }
}

/** 服务器权限模式仅接受稳定枚举，未知值保持本地选择。 */
export function normalizedPermissionMode(value: string | undefined): PermissionMode | undefined {
  return value === 'full_approval' || value === 'auto_approval' || value === 'full_access' ? value : undefined
}

/**
 * mergeDetail 合并会话详情：不倒退轮次数，也不让后到的默认标题覆盖已生成标题。
 * 返回 null 表示详情过旧应整体丢弃。
 */
export function mergeDetail(current: Conversation | undefined, detail: Conversation): Conversation | null {
  if (current && current.id !== detail.id) return null
  if (current && current.turn_count > detail.turn_count) return null
  if (!current) return detail
  const { value } = mergeTitle(current, titleFromConversation(detail))
  // 无版本旧接口继续保留已经生成的标题；显式更高版本的默认文本改名仍可生效。
  const title = current.title_revision === undefined && detail.title_revision === undefined && current.title !== '新对话' && detail.title === '新对话' ? current.title : value.title
  return { ...detail, title, title_revision: value.title_revision, title_job_status: value.title_job_status, title_job_revision: value.title_job_revision }
}

/** upsertApproval 保留既有裁决结果，避免迟到的 pending 帧回退已终态卡片。 */
export function upsertApproval(current: Map<string, Approval>, approval: Approval): Map<string, Approval> {
  const existing = current.get(approval.approval_id)
  if (existing && existing.status !== 'pending' && approval.status === 'pending') return current
  if (existing?.status === 'consumed' && approval.status !== 'consumed') return current
  if (existing && ['rejected', 'expired', 'canceled'].includes(existing.status) && existing.status !== approval.status) return current
  if (existing && ['succeeded', 'failed', 'canceled'].includes(existing.execution_state ?? '') &&
    existing.execution_state !== approval.execution_state) return current
  return new Map(current).set(approval.approval_id, { ...existing, ...approval })
}
