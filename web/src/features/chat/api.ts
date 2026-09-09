import { ApiRequestError, buildUrl, del, get, post, put } from '../../api/http'
import type { ApiResponse } from '../../api/http'
import { consumeSSE } from './sse'
import type { ChatFrame, Conversation, ConversationContext, ConversationPage, ConversationTitle, TurnPage } from './types'

const PATH = '/v1/chat/conversation'

export function fetchConversations(keyword: string, favorite: boolean, page: number, signal?: AbortSignal) {
  return get<ConversationPage>(`${PATH}/page`, { keyword, favorite: favorite || undefined, page, page_size: 20 }, signal)
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
  return get<TurnPage>(`${PATH}/${encodeURIComponent(id)}/turns`, { before, limit: 20 }, signal)
}
export function updateConversation(id: string, payload: { title?: string; favorite?: boolean }) {
  return put<Conversation>(`${PATH}/${encodeURIComponent(id)}`, payload)
}
export function deleteConversation(id: string) {
  return del(`${PATH}/${encodeURIComponent(id)}`)
}
export async function streamChat(id: string, messages: string, signal: AbortSignal, onFrame: (frame: ChatFrame) => void, modelId?: string) {
  const response = await fetch(buildUrl('/v1/chat/sse'), {
    method: 'POST',
    headers: { Accept: 'text/event-stream', 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify({ id: id || undefined, messages, flag: 'chat', model_id: modelId || undefined }),
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
