import { fetchConversation, generateConversationTitle } from './api'
import type { Conversation, ConversationTitle } from './types'

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
