import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchConversations } from './api'
import type { Conversation, ConversationTitle } from './types'

export function useConversations() {
  const [keyword, setKeyword] = useState('')
  const [favorite, setFavorite] = useState(false)
  const [page, setPage] = useState(1)
  const [version, setVersion] = useState(0)
  const [items, setItems] = useState<Conversation[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const request = useRef<AbortController | null>(null)
  const titleUpdates = useRef(new Map<string, string>())
  const refresh = useCallback(() => setVersion(value => value + 1), [])

  const load = useCallback(async (foreground: boolean) => {
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    // Apply local patches to requests already in flight, never to future reads.
    const patches = new Map<string, string>()
    titleUpdates.current = patches
    if (foreground) setLoading(true)
    setError('')
    try {
      const result = await fetchConversations(keyword, favorite, page, controller.signal)
      if (controller.signal.aborted) return
      setItems((result.items ?? []).map(item => patches.has(item.id) ? { ...item, title: patches.get(item.id)! } : item))
      setTotal(result.total)
    } catch (error) {
      if (!controller.signal.aborted) setError(error instanceof Error ? error.message : '加载会话失败')
    } finally {
      if (!controller.signal.aborted) setLoading(false)
    }
  }, [keyword, favorite, page])

  useEffect(() => {
    const timer = setTimeout(() => void load(true), 250)
    return () => { clearTimeout(timer); request.current?.abort() }
  }, [load, version])

  // Chat completions must use the latest filter/page, not the closure at send time.
  const latestLoad = useRef(load)
  useEffect(() => { latestLoad.current = load }, [load])
  const refreshQuietly = useCallback(() => latestLoad.current(false), [])
  const updateTitle = useCallback(({ id, title }: ConversationTitle) => {
    titleUpdates.current.set(id, title)
    setItems(current => current.map(item => item.id === id ? { ...item, title } : item))
  }, [])

  return {
    items, total, loading, error, keyword, favorite, page, refresh, refreshQuietly, updateTitle, setPage,
    search: (value: string) => { setKeyword(value); setPage(1) },
    filter: (value: boolean) => { setFavorite(value); setPage(1) },
  }
}
