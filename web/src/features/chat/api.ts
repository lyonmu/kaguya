import { ApiRequestError, buildUrl, del, get, post, put } from '../../api/http'
import type { ApiResponse } from '../../api/http'
import { consumeSSE } from './sse'
import type { ChatFrame, Conversation, ConversationContext, ConversationPage, ConversationTitle, TurnPage, Block } from './types'

export const HISTORY_PAGE_SIZE = 5

const PATH = '/v1/chat/conversation'

export function fetchConversations(keyword: string, favorite: boolean, page: number, signal?: AbortSignal, projectId?: string) {
  return get<ConversationPage>(`${PATH}/page`, { keyword, favorite: favorite || undefined, is_project: !!projectId, project_id: projectId || undefined, page, page_size: 20 }, signal)
}
export function fetchConversation(id: string, signal?: AbortSignal) {
  return get<Conversation>(`${PATH}/${encodeURIComponent(id)}`, undefined, signal)
}
export function fetchConversationContext(id: string, signal?: AbortSignal) {
  return get<ConversationContext>(`${PATH}/${encodeURIComponent(id)}/context`, undefined, signal)
}
export function generateConversationTitle(id: string, signal?: AbortSignal) {
  return post<ConversationTitle>(`${PATH}/${encodeURIComponent(id)}/title/wait`, undefined, signal)
}
export function fetchTurns(id: string, before = 0, signal?: AbortSignal) {
  return get<TurnPage>(`${PATH}/${encodeURIComponent(id)}/turns`, { before, limit: HISTORY_PAGE_SIZE, compact: true }, signal)
}
export function fetchTurnPage(id: string, page: number, signal?: AbortSignal) {
  return get<TurnPage>(`${PATH}/${encodeURIComponent(id)}/turns`, { page, limit: HISTORY_PAGE_SIZE, compact: true }, signal)
}
export function updateConversation(id: string, payload: { title?: string; favorite?: boolean }) {
  return put<Conversation>(`${PATH}/${encodeURIComponent(id)}`, payload)
}
// stopConversation 标记用户主动停止该会话的当前轮次，供服务端区分 canceled 与断联；
// 取消本身仍通过断开 SSE 完成。
export function stopConversation(id: string) {
  return post<null>(`${PATH}/${encodeURIComponent(id)}/stop`)
}
export function deleteConversation(id: string) {
  return del(`${PATH}/${encodeURIComponent(id)}`)
}
export async function streamChat(id: string, messages: string, signal: AbortSignal, onFrame: (frame: ChatFrame) => void, modelId?: string, projectId?: string, files: string[] = []) {
  const response = await fetch(buildUrl('/v1/chat/sse'), {
    method: 'POST',
    headers: { Accept: 'text/event-stream', 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify({ id: id || undefined, messages, flag: 'chat', model_id: modelId || undefined, project_id: projectId || undefined, files: projectId && files.length ? files : undefined }),
    signal,
  })
  if (!response.ok || !response.headers.get('content-type')?.includes('text/event-stream')) {
    let payload: ApiResponse<unknown> | undefined
    try { payload = await response.json() } catch { /* Non-JSON HTTP error. */ }
    throw new ApiRequestError(payload?.message || `对话请求失败（${response.status}）`, { status: response.status, code: payload?.code })
  }
  if (!response.body) throw new ApiRequestError('浏览器不支持流式响应')
  await consumeSSE(response.body, onFrame)
}

export function searchProjectFiles(projectId: string, query: string, signal?: AbortSignal) {
  return get<{ files: string[]; truncated: boolean }>(`/v1/project/${encodeURIComponent(projectId)}/files`, { query }, signal)
}

export function fetchBlock(id: string, turn: number, sequence: number, signal?: AbortSignal) {
  return get<Block>(`${PATH}/${encodeURIComponent(id)}/turns/${turn}/blocks/${sequence}`, undefined, signal)
}
