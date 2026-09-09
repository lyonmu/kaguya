import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchConversations } from './api'
import type { Conversation, ConversationTitle } from './types'

export function useConversations(projectId?: string) {
  const [keyword, setKeyword] = useState('')
  const [favorite, setFavorite] = useState(false)
  const page = useRef(1)
  const busy = useRef(false)
  const [version, setVersion] = useState(0)
  const [items, setItems] = useState<Conversation[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const request = useRef<AbortController | null>(null)
  const titleUpdates = useRef(new Map<string, string>())
  const refresh = useCallback(() => setVersion(value => value + 1), [])

  const load = useCallback(async (foreground: boolean, append = false) => {
    if (append && busy.current) return
    busy.current = true
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    // Apply local patches to requests already in flight, never to future reads.
    const patches = new Map<string, string>()
    titleUpdates.current = patches
    if (foreground) setLoading(true)
    setError('')
    try {
      const nextPage = append ? page.current + 1 : page.current
      const results = await Promise.all((append ? [nextPage] : Array.from({ length: nextPage }, (_, index) => index + 1))
        .map(value => fetchConversations(keyword, favorite, value, controller.signal, projectId)))
      if (controller.signal.aborted) return
      const loaded = results.flatMap(result => result.items ?? []).map(item => patches.has(item.id) ? { ...item, title: patches.get(item.id)! } : item)
      setItems(current => [...new Map((append ? [...current, ...loaded] : loaded).map(item => [item.id, item])).values()])
      page.current = nextPage
      setTotal(results[0].total)
    } catch (error) {
      if (!controller.signal.aborted) setError(error instanceof Error ? error.message : '加载会话失败')
    } finally {
      if (!controller.signal.aborted) { setLoading(false); busy.current = false }
    }
  }, [keyword, favorite, projectId])

  useEffect(() => {
    page.current = 1
    setItems([])
    setTotal(0)
  }, [projectId])

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
    items, total, loading, error, keyword, favorite, refresh, refreshQuietly, updateTitle,
    loadMore: () => { if (!loading && !error && items.length < total) void load(true, true) },
    search: (value: string) => { request.current?.abort(); page.current = 1; setItems([]); setTotal(0); setLoading(true); setKeyword(value) },
    filter: (value: boolean) => { request.current?.abort(); page.current = 1; setItems([]); setTotal(0); setLoading(true); setFavorite(value) },
  }
}
