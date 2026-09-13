import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchConversations } from './api'
import type { Conversation, ConversationTitle } from './types'

// 会话列表按服务端真实分页累积到内存：每页独立保存，刷新只重取首尾页，
// 中间页保持不变（见 loadPages），避免已加载的页被刷新结果覆盖丢失。
export function useConversations(projectId?: string, options?: { enabled?: boolean }) {
  const enabled = options?.enabled ?? true
  const [keyword, setKeyword] = useState('')
  const [favorite, setFavorite] = useState(false)
  const nextPage = useRef(1)
  const busy = useRef(false)
  const [version, setVersion] = useState(0)
  const [items, setItems] = useState<Conversation[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const request = useRef<AbortController | null>(null)
  const pages = useRef(new Map<number, Conversation[]>())
  const titleUpdates = useRef(new Map<string, string>())
  const refresh = useCallback(() => setVersion(value => value + 1), [])

  // 同一筛选条件的页请求必须复用同一个信号与函数，刷新不得放大成大量并发请求。
  const fetchPage = useCallback((value: number, signal: AbortSignal) =>
    fetchConversations(keyword, favorite, value, signal, projectId), [keyword, favorite, projectId])

  const syncItems = useCallback(() => {
    const merged: Conversation[] = []
    const seen = new Set<string>()
    for (const number of [...pages.current.keys()].sort((a, b) => a - b)) {
      for (const item of pages.current.get(number) ?? []) {
        if (seen.has(item.id)) continue
        seen.add(item.id)
        const title = titleUpdates.current.get(item.id)
        merged.push(title === undefined ? item : { ...item, title })
      }
    }
    setItems(merged)
  }, [])

  // 刷新只并发拉取有限页：第一页给出最新列表与总数，再补上用户已看到的末尾页；
  // 中间页保持原数据，避免 30 页触发 30 个并发请求。
  const REFRESH_HEAD_PAGES = 2
  const REFRESH_TAIL_PAGES = 2
  const REFRESH_CONCURRENCY = 4
  const loadPages = useCallback(async (signal: AbortSignal) => {
    const loaded = nextPage.current - 1
    const head = Math.min(REFRESH_HEAD_PAGES, loaded)
    const tailStart = Math.max(head + 1, loaded - REFRESH_TAIL_PAGES + 1)
    const numbers = loaded === 0
      ? [1]
      : [...Array.from({ length: head }, (_, index) => index + 1), ...Array.from({ length: Math.max(0, loaded - tailStart + 1) }, (_, index) => tailStart + index)]
    const results: Array<Awaited<ReturnType<typeof fetchPage>> | undefined> = new Array(numbers.length)
    let cursor = 0
    const worker = async () => {
      while (!signal.aborted) {
        const index = cursor++
        if (index >= numbers.length) return
        results[index] = await fetchPage(numbers[index], signal)
      }
    }
    await Promise.all(Array.from({ length: Math.min(REFRESH_CONCURRENCY, numbers.length) }, worker))
    if (signal.aborted) return
    const first = results.find(result => result !== undefined)
    if (first) setTotal(first.total)
    results.forEach((result, index) => { if (result) pages.current.set(numbers[index], result.items) })
    // 首次加载或刷新同时推进下一页游标，确保 loadMore 不会重复请求已取回的页。
    nextPage.current = numbers[numbers.length - 1] + 1
    syncItems()
  }, [fetchPage, syncItems])

  const load = useCallback(async (foreground: boolean, append = false) => {
    if (append && busy.current) return
    busy.current = true
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    if (foreground) setLoading(true)
    setError('')
    try {
      if (append) {
        const result = await fetchPage(nextPage.current, controller.signal)
        if (controller.signal.aborted) return
        pages.current.set(nextPage.current, result.items)
        nextPage.current += 1
        setTotal(result.total)
        syncItems()
      } else {
        await loadPages(controller.signal)
      }
    } catch (error) {
      if (!controller.signal.aborted) setError(error instanceof Error ? error.message : '加载会话失败')
    } finally {
      if (!controller.signal.aborted) { setLoading(false); busy.current = false }
    }
  }, [fetchPage, loadPages, syncItems])

  useEffect(() => {
    nextPage.current = 1
    pages.current.clear()
    setItems([])
    setTotal(0)
  }, [projectId])

  useEffect(() => {
    if (!enabled) return
    const timer = setTimeout(() => void load(true), 250)
    return () => { clearTimeout(timer); request.current?.abort() }
  }, [load, version, enabled])

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
    resetFilters: () => { request.current?.abort(); nextPage.current = 1; pages.current.clear(); setKeyword(''); setFavorite(false); setItems([]); setTotal(0); setLoading(true); refresh() },
    loadMore: () => { if (enabled && !loading && !error && items.length < total) void load(true, true) },
    search: (value: string) => { request.current?.abort(); nextPage.current = 1; pages.current.clear(); setItems([]); setTotal(0); setLoading(true); setKeyword(value) },
    filter: (value: boolean) => { request.current?.abort(); nextPage.current = 1; pages.current.clear(); setItems([]); setTotal(0); setLoading(true); setFavorite(value) },
  }
}
