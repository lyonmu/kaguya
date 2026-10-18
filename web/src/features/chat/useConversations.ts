import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchConversations } from './api'
import type { Conversation, TitleUpdate } from './types'
import { mergeTitle, titleFromConversation } from './titleMerge'
import { useTitleScope } from './titleSyncContext'

// 页中的同版本冲突不能替换已经确认的overlay，仍由协调器补查。
function withOverlay(base: Conversation, overlay: TitleUpdate): Conversation {
  const merged = mergeTitle(base, overlay).value
  return { ...merged,
    title: overlay.title !== undefined && overlay.revision !== undefined && base.title_revision === overlay.revision ? overlay.title : merged.title,
    title_job_status: overlay.job_status !== undefined && overlay.job_revision !== undefined && base.title_job_revision === overlay.job_revision ? overlay.job_status : merged.title_job_status }
}

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
  const [titleError, setTitleError] = useState('')
  const request = useRef<AbortController | null>(null)
  const mounted = useRef(true)
  const pages = useRef(new Map<number, Conversation[]>())
  const titleUpdates = useRef(new Map<string, TitleUpdate>())
  const removed = useRef(new Set<string>())
  const conflict = useRef<(id: string) => void>(() => {})
  const refresh = useCallback(() => setVersion(value => value + 1), [])

  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])

  const cancelCurrent = useCallback((finishLoading: boolean) => {
    const controller = request.current
    controller?.abort()
    if (request.current === controller) {
      request.current = null
      busy.current = false
      if (finishLoading && mounted.current) setLoading(false)
    }
  }, [])

  // 同一筛选条件的页请求必须复用同一个信号与函数，刷新不得放大成大量并发请求。
  const fetchPage = useCallback((value: number, signal: AbortSignal) =>
    fetchConversations(keyword, favorite, value, signal, projectId), [keyword, favorite, projectId])

  const syncItems = useCallback(() => {
    const merged: Conversation[] = []
    const seen = new Set<string>()
    for (const number of [...pages.current.keys()].sort((a, b) => a - b)) {
      for (const item of pages.current.get(number) ?? []) {
        if (seen.has(item.id) || removed.current.has(item.id)) continue
        seen.add(item.id)
        const overlay = titleUpdates.current.get(item.id)
        if (!overlay) { merged.push(item); continue }
        const pending = { ...overlay }
        let base = item
        if (overlay.title !== undefined) {
          if (item.title_revision !== undefined && overlay.revision !== undefined && item.title_revision >= overlay.revision) {
            if (item.title_revision === overlay.revision && item.title !== overlay.title) {
              base = { ...base, title: overlay.title }
              conflict.current(item.id)
            } else { delete pending.title; delete pending.revision }
          } else if (item.title_revision === undefined && overlay.revision === undefined && item.title === overlay.title) { delete pending.title; delete pending.revision }
        }
        if (overlay.job_status !== undefined) {
          if (item.title_job_revision !== undefined && overlay.job_revision !== undefined && item.title_job_revision >= overlay.job_revision) {
            if (item.title_job_revision === overlay.job_revision && (item.title_job_status ?? null) !== overlay.job_status) {
              base = { ...base, title_job_status: overlay.job_status }
              conflict.current(item.id)
            } else { delete pending.job_status; delete pending.job_revision }
          }
        }
        if (pending.title === undefined && pending.job_status === undefined) titleUpdates.current.delete(item.id)
        else titleUpdates.current.set(item.id, pending)
        merged.push(mergeTitle(base, pending).value)
      }
    }
    setItems(merged)
  }, [])

  const updateTitle = useCallback((update: TitleUpdate) => {
    if (removed.current.has(update.id)) return false
    const base = [...pages.current.values()].flatMap(page => page).find(item => item.id === update.id)
    if (!base) return false
    const current = withOverlay(base, titleUpdates.current.get(update.id) ?? { id: update.id })
    const merged = mergeTitle(current, update)
    titleUpdates.current.set(update.id, titleFromConversation(merged.value))
    syncItems()
    return merged.conflict
  }, [syncItems])
  const removeTitleID = useCallback((id: string) => {
    // 终止已开始的页请求；同scope移除标记还保护尚未提交的React状态。
    cancelCurrent(true)
    removed.current.add(id)
    titleUpdates.current.delete(id)
    for (const [number, page] of pages.current) pages.current.set(number, page.filter(item => item.id !== id))
    syncItems()
  }, [cancelCurrent, syncItems])
  const titleSync = useTitleScope(items.filter(item => !removed.current.has(item.id)).map(item => item.id), { update: updateTitle, missing: removeTitleID, error: setTitleError, clearError: () => setTitleError('') }, enabled)
  conflict.current = id => titleSync?.conflict(id)

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
    if (!enabled || !mounted.current || (append && busy.current)) return
    busy.current = true
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    if (foreground) setLoading(true)
    setError('')
    setTitleError('')
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
      if (!controller.signal.aborted && request.current === controller) setError(error instanceof Error ? error.message : '加载会话失败')
    } finally {
      if (request.current === controller) {
        request.current = null
        busy.current = false
        if (mounted.current) setLoading(false)
      }
    }
  }, [enabled, fetchPage, loadPages, syncItems])

  useEffect(() => {
    nextPage.current = 1
    pages.current.clear()
    titleUpdates.current.clear()
    removed.current.clear()
    setItems([])
    setTotal(0)
  }, [projectId])

  useEffect(() => {
    if (!enabled) {
      cancelCurrent(true)
      return
    }
    const timer = setTimeout(() => void load(true), 250)
    return () => { clearTimeout(timer); cancelCurrent(false) }
  }, [load, version, enabled, cancelCurrent])

  // Chat completions must use the latest filter/page, not the closure at send time.
  const latestLoad = useRef(load)
  useEffect(() => { latestLoad.current = load }, [load])
  const refreshQuietly = useCallback(() => latestLoad.current(false), [])
  return {
    items, total, loading, error, titleError, keyword, favorite, refresh, refreshQuietly, updateTitle,
    resetFilters: () => { cancelCurrent(false); nextPage.current = 1; pages.current.clear(); titleUpdates.current.clear(); removed.current.clear(); setKeyword(''); setFavorite(false); setItems([]); setTotal(0); setLoading(enabled); refresh() },
    loadMore: () => { if (enabled && !loading && !error && items.length < total) void load(true, true) },
    search: (value: string) => { cancelCurrent(false); nextPage.current = 1; pages.current.clear(); titleUpdates.current.clear(); removed.current.clear(); setItems([]); setTotal(0); setLoading(enabled); setKeyword(value); if (value === keyword) refresh() },
    filter: (value: boolean) => { cancelCurrent(false); nextPage.current = 1; pages.current.clear(); titleUpdates.current.clear(); removed.current.clear(); setItems([]); setTotal(0); setLoading(enabled); setFavorite(value); if (value === favorite) refresh() },
  }
}
