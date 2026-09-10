import { createContext, useContext, useEffect, type RefObject } from 'react'
import type { useChat } from './useChat'
import type { ConversationTitle } from './types'

export type Callbacks = { onCompleted: () => Promise<void>; onTitleUpdated: (title: ConversationTitle) => void }
export const ChatContext = createContext<{ chat: ReturnType<typeof useChat>; callbacks: RefObject<Callbacks | undefined> } | undefined>(undefined)

export function useWorkspaceChat(onCompleted: Callbacks['onCompleted'], onTitleUpdated: Callbacks['onTitleUpdated']) {
  const context = useContext(ChatContext)
  if (!context) throw new Error('ChatProvider is required')
  useEffect(() => {
    const value = { onCompleted, onTitleUpdated }
    const callbacks = context.callbacks
    callbacks.current = value
    return () => { if (callbacks.current === value) callbacks.current = undefined }
  }, [context.callbacks, onCompleted, onTitleUpdated])
  return context.chat
}
