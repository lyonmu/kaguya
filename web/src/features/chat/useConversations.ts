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

  // 同一筛选条件的页请求必须复用同一个信号与函数，刷新不得放大成大量并发请求。
  const fetchPage = useCallback((value: number, signal: AbortSignal) =>
    fetchConversations(keyword, favorite, value, signal, projectId), [keyword, favorite, projectId])

  // 刷新只并发拉取有限页：第一页给出最新列表与总数，再补上用户已看到的末尾页；
  // 中间页保持原数据，避免 30 页触发 30 个并发请求。
  const REFRESH_HEAD_PAGES = 2
  const REFRESH_TAIL_PAGES = 2
  const REFRESH_CONCURRENCY = 4
  const loadPages = useCallback(async (signal: AbortSignal) => {
    const loaded = page.current
    // 刷新只重取第一页与用户已看到的末尾页；中间页保留，翻页时自然更新。
    const pages = loaded <= REFRESH_HEAD_PAGES
      ? Array.from({ length: loaded }, (_, index) => index + 1)
      : [...Array.from({ length: REFRESH_HEAD_PAGES }, (_, index) => index + 1), ...Array.from({ length: Math.min(REFRESH_TAIL_PAGES, loaded - REFRESH_HEAD_PAGES) }, (_, index) => loaded - REFRESH_TAIL_PAGES + index + 1)]
    const results: Array<Awaited<ReturnType<typeof fetchPage>> | undefined> = new Array(pages.length)
    let cursor = 0
    const worker = async () => {
      while (!signal.aborted) {
        const index = cursor++
        if (index >= pages.length) return
        results[index] = await fetchPage(pages[index], signal)
      }
    }
    await Promise.all(Array.from({ length: Math.min(REFRESH_CONCURRENCY, pages.length) }, worker))
    if (signal.aborted) return []
    const first = results.find(result => result !== undefined)
    if (first) setTotal(first.total)
    return results.flatMap(result => result?.items ?? [])
  }, [fetchPage])

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
      const loaded = append
        ? (await fetchPage(page.current + 1, controller.signal))?.items ?? []
        : await loadPages(controller.signal)
      if (controller.signal.aborted) return
      const patched = loaded.map(item => patches.has(item.id) ? { ...item, title: patches.get(item.id)! } : item)
      setItems(current => [...new Map((append ? [...current, ...patched] : patched).map(item => [item.id, item])).values()])
      if (append) page.current += 1
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
    resetFilters: () => { request.current?.abort(); page.current = 1; setKeyword(''); setFavorite(false); setItems([]); setTotal(0); setLoading(true); refresh() },
    loadMore: () => { if (!loading && !error && items.length < total) void load(true, true) },
    search: (value: string) => { request.current?.abort(); page.current = 1; setItems([]); setTotal(0); setLoading(true); setKeyword(value) },
    filter: (value: boolean) => { request.current?.abort(); page.current = 1; setItems([]); setTotal(0); setLoading(true); setFavorite(value) },
  }
}
