import { useRef, type ReactNode } from 'react'
import { useChat } from './useChat'
import { ChatContext, type Callbacks } from './chatContext'

// Above navigation: visiting settings must not unmount and cancel active streams.
export function ChatProvider({ children }: { children: ReactNode }) {
  const callbacks = useRef<Callbacks | undefined>(undefined)
  const chat = useChat(async () => { await callbacks.current?.onCompleted() }, title => callbacks.current?.onTitleUpdated(title))
  return <ChatContext.Provider value={{ chat, callbacks }}>{children}</ChatContext.Provider>
}

