import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useChat } from './useChat'
import { ChatContext, type Callbacks } from './chatContext'
import { useTitleEvents } from './useTitleEvents'
import { TitleSync } from './titleSync'
import { TitleSyncContext } from './titleSyncContext'
import { titleFromEvent } from './titleMerge'

// Above navigation: visiting settings must not unmount and cancel active streams.
export function ChatProvider({ children }: { children: ReactNode }) {
  const callbacks = useRef<Callbacks | undefined>(undefined)
  const [sync] = useState(() => new TitleSync())
  const chat = useChat(async () => { await callbacks.current?.onCompleted() }, title => sync.update(title), id => sync.conflict(id))
  const latest = useRef(chat)
  latest.current = chat
  const scope = useRef<ReturnType<TitleSync['register']> | undefined>(undefined)
  useEffect(() => {
    const registered = sync.register({ update: value => latest.current.applyTitleEvent(value), missing: id => latest.current.removeConversation(id), error: message => latest.current.titleSyncError(message), clearError: () => latest.current.titleSyncError('') })
    scope.current = registered
    registered.setIDs(latest.current.knownConversationIDs)
    return () => { registered.close(); if (scope.current === registered) scope.current = undefined }
  }, [sync])
  useEffect(() => { scope.current?.setIDs(chat.knownConversationIDs) }, [chat.knownConversationIDs])
  useTitleEvents(event => sync.update(titleFromEvent(event)), signal => sync.connect(signal), () => latest.current.titleSyncError('标题事件连接中断，正在重连'))
  return <TitleSyncContext.Provider value={sync}><ChatContext.Provider value={{ chat, callbacks }}>{children}</ChatContext.Provider></TitleSyncContext.Provider>
}
