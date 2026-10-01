import { useRef, type ReactNode } from 'react'
import { useChat } from './useChat'
import { ChatContext, type Callbacks } from './chatContext'
import { useTitleEvents } from './useTitleEvents'

// Above navigation: visiting settings must not unmount and cancel active streams.
export function ChatProvider({ children }: { children: ReactNode }) {
  const callbacks = useRef<Callbacks | undefined>(undefined)
  const chat = useChat(async () => { await callbacks.current?.onCompleted() }, title => callbacks.current?.onTitleUpdated(title))
  // 标题事件按 conversation_id 同时更新 session 与列表；先订阅再补快照的竞态由
  // 调用方的列表加载（初始加载与重连刷新）消解。
  useTitleEvents(event => {
    const title = event.title ?? ''
    if (!title) return
    chat.applyTitleEvent({ id: event.conversation_id, title, revision: event.revision })
    callbacks.current?.onTitleUpdated({ id: event.conversation_id, title, revision: event.revision })
  })
  return <ChatContext.Provider value={{ chat, callbacks }}>{children}</ChatContext.Provider>
}

