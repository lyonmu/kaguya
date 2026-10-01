import type { Approval, Conversation, PermissionMode, Reference, Turn } from './types'

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
  return { key: `session-${++nextKey}`, id, draft: '', references: [], approvals: new Map(), permissionMode: 'full_access', modelId: '', turns: [], page: 1, totalPages: 0, initialEnd: true, loading: false, streaming: false, error: '', opVersion: 0 }
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
  if (current && current.turn_count > detail.turn_count) return null
  return current?.title && current.title !== '新对话' && detail.title === '新对话' ? { ...detail, title: current.title } : detail
}

/** upsertApproval 保留既有裁决结果，避免迟到的 pending 帧回退已终态卡片。 */
export function upsertApproval(current: Map<string, Approval>, approval: Approval): Map<string, Approval> {
  const existing = current.get(approval.approval_id)
  if (existing && existing.status !== 'pending' && approval.status === 'pending') return current
  return new Map(current).set(approval.approval_id, approval)
}
