import { fetchConversation, generateConversationTitle } from './api'
import type { Conversation, ConversationTitle } from './types'

// 首轮 start 后立即读取会话详情，并等待后端在正文开始时并行启动的标题任务。
// 标题是辅助信息：失败或未配置任务模型时保留“新对话”，done 后仍会兜底重试。
export async function syncStartedConversation(
  id: string,
  signal: AbortSignal,
  callbacks: {
    onDetail: (detail: Conversation) => void
    onTitle: (title: ConversationTitle) => void
  },
) {
  const detail = await fetchConversation(id, signal)
  signal.throwIfAborted()
  callbacks.onDetail(detail)
  const title = await generateConversationTitle(id, signal)
  signal.throwIfAborted()
  if (title.title !== detail.title) callbacks.onTitle(title)
}

// Check the persisted title after every successful turn, never poll within a turn.
// A default title can retry generation; completed titles only refresh runtime data.
export async function syncCompletedConversation(
  id: string,
  signal: AbortSignal,
  callbacks: {
    onDetail: (detail: Conversation) => void
    onCompleted: () => Promise<void>
    onTitle: (title: ConversationTitle) => void
  },
) {
  const detail = await fetchConversation(id, signal)
  signal.throwIfAborted()
  callbacks.onDetail(detail)
  await callbacks.onCompleted()
  signal.throwIfAborted()
  if (detail.title === '新对话') {
    const title = await generateConversationTitle(id, signal)
    signal.throwIfAborted()
    callbacks.onTitle(title)
  }
}
