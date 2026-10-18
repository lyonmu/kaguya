import { createContext, useContext, useEffect, useRef } from 'react'
import type { TitleSync } from './titleSync'
import type { TitleUpdate } from './types'

export const TitleSyncContext = createContext<TitleSync | undefined>(undefined)

/** 列表自己拥有实体；scope在enabled生命周期注册，ID变化只更新成员。 */
export function useTitleScope(ids: readonly string[], handlers: { update: (value: TitleUpdate) => boolean; missing: (id: string) => void; error: (message: string) => void; clearError?: () => void }, enabled = true) {
  const sync = useContext(TitleSyncContext)
  const latest = useRef({ ids, handlers })
  latest.current = { ids, handlers }
  const scope = useRef<ReturnType<TitleSync['register']> | undefined>(undefined)
  useEffect(() => {
    if (!sync || !enabled) return
    const registered = sync.register({ update: value => latest.current.handlers.update(value), missing: id => latest.current.handlers.missing(id), error: message => latest.current.handlers.error(message), clearError: () => latest.current.handlers.clearError?.() })
    scope.current = registered
    registered.setIDs(latest.current.ids)
    return () => { registered.close(); if (scope.current === registered) scope.current = undefined }
  }, [sync, enabled])
  useEffect(() => { scope.current?.setIDs(ids) }, [ids])
  return sync
}
