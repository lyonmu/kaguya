import { del, get, post, put } from '../../api/http'
import { openStream } from '../../api/stream'
import { approvalDecisionGuard, approvalListGuard, blockGuard, conversationContextGuard, conversationGuard, conversationPageGuard, conversationTitleGuard, titleSnapshotsGuard, turnPageGuard } from './guards'
import { consumeSSE, type ConversationSSEOptions } from './sse'
import type { Approval, ApprovalDecision, ChatFrame, Conversation, ConversationContext, ConversationPage, ConversationTitle, Reference, TurnPage, Block } from './types'

export const HISTORY_PAGE_SIZE = 5

const PATH = '/v1/chat/conversation'

export function fetchConversations(keyword: string, favorite: boolean, page: number, signal?: AbortSignal, projectId?: string) {
  return get<ConversationPage>(`${PATH}/page`, { keyword, favorite: favorite || undefined, is_project: !!projectId, project_id: projectId || undefined, page, page_size: 20 }, signal, conversationPageGuard)
}
export function fetchConversation(id: string, signal?: AbortSignal) {
  return get<Conversation>(`${PATH}/${encodeURIComponent(id)}`, undefined, signal, conversationGuard)
}
export function fetchConversationContext(id: string, signal?: AbortSignal) {
  return get<ConversationContext>(`${PATH}/${encodeURIComponent(id)}/context`, undefined, signal, conversationContextGuard)
}
export function generateConversationTitle(id: string, signal?: AbortSignal) {
  return post<ConversationTitle>(`${PATH}/${encodeURIComponent(id)}/title/wait`, undefined, signal, conversationTitleGuard)
}
export function fetchTitleSnapshots(ids: string[], signal: AbortSignal) {
  if (!ids.length || ids.length > 200) throw new Error('标题快照请求数量不正确')
  return post(`${PATH}/title-snapshots`, { ids }, signal, titleSnapshotsGuard(ids))
}
export function fetchTurns(id: string, before = 0, signal?: AbortSignal) {
  return get<TurnPage>(`${PATH}/${encodeURIComponent(id)}/turns`, { before, limit: HISTORY_PAGE_SIZE, compact: true }, signal, turnPageGuard)
}
export function fetchTurnPage(id: string, page: number, signal?: AbortSignal) {
  return get<TurnPage>(`${PATH}/${encodeURIComponent(id)}/turns`, { page, limit: HISTORY_PAGE_SIZE, compact: true }, signal, turnPageGuard)
}
export function updateConversation(id: string, payload: { title?: string; favorite?: boolean; memory_mode?: string; permission_mode?: string }) {
  return put<Conversation>(`${PATH}/${encodeURIComponent(id)}`, payload, undefined, conversationGuard)
}
// stopConversation 标记用户主动停止该会话的当前轮次，供服务端区分 canceled 与断联；
// 取消本身仍通过断开 SSE 完成。
export function stopConversation(id: string) {
  return post<null>(`${PATH}/${encodeURIComponent(id)}/stop`)
}
// 旧合并指令快照的显式升级：归档旧文本并冻结当前项目指令，下一轮生效。
export function upgradeConversationInstructions(id: string) {
  return post<Conversation>(`${PATH}/${encodeURIComponent(id)}/instructions/upgrade`, undefined, undefined, conversationGuard)
}
// 审批裁决：一次调用仅生效一次；相同裁决幂等，冲突裁决由后端返回明确错误。
export function decideApproval(id: string, approvalId: string, decision: 'approve' | 'reject', signal?: AbortSignal) {
  return post<ApprovalDecision>(`${PATH}/${encodeURIComponent(id)}/approvals/${encodeURIComponent(approvalId)}`, { decision }, signal, approvalDecisionGuard)
}
// 当前等待裁决的审批；仅作页面刷新后的状态补齐。
export function fetchApprovals(id: string, signal?: AbortSignal) {
  return get<Approval[]>(`${PATH}/${encodeURIComponent(id)}/approvals`, undefined, signal, approvalListGuard)
}
export function deleteConversation(id: string) {
  return del(`${PATH}/${encodeURIComponent(id)}`)
}
export async function streamChat(id: string, messages: string, signal: AbortSignal, onFrame: (frame: ChatFrame) => void, modelId?: string, projectId?: string, references: Reference[] = [], permissionMode?: string, onErrorAudit?: ConversationSSEOptions['onErrorAudit']) {
  const body = await openStream('/v1/chat/sse', {
    method: 'POST',
    signal,
    body: {
      id: id || undefined,
      messages,
      model_id: modelId || undefined,
      project_id: projectId || undefined,
      references: projectId && references.length ? references : undefined,
      permission_mode: !id && permissionMode ? permissionMode : undefined,
    },
  })
  await consumeSSE(body, onFrame, { conversationId: id || undefined, onErrorAudit })
}

export function fetchBlock(id: string, turn: number, sequence: number, signal?: AbortSignal) {
  return get<Block>(`${PATH}/${encodeURIComponent(id)}/turns/${turn}/blocks/${sequence}`, undefined, signal, blockGuard)
}
