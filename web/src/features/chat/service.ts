import { fetchConversation } from './api'
import type { Conversation } from './types'

// 正常start只同步详情，标题由事件/快照同步，显式补生成走独立操作。
export async function syncStartedConversation(
  id: string,
  signal: AbortSignal,
  callbacks: {
    onDetail: (detail: Conversation) => void
  },
) {
  const detail = await fetchConversation(id, signal)
  signal.throwIfAborted()
  callbacks.onDetail(detail)
}

// Check the persisted title after every successful turn, never poll within a turn.
// 标题任务不会阻塞轮次完成后的列表刷新。
export async function syncCompletedConversation(
  id: string,
  signal: AbortSignal,
  callbacks: {
    onDetail: (detail: Conversation) => void
    onCompleted: () => Promise<void>
  },
) {
  const detail = await fetchConversation(id, signal)
  signal.throwIfAborted()
  callbacks.onDetail(detail)
  await callbacks.onCompleted()
  signal.throwIfAborted()
}
