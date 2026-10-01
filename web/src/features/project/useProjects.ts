import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchProjects } from './api'
import type { Project } from './types'

/**
 * useProjects 管理项目列表：服务端分页、滚动加载、关键词去抖搜索与刷新。
 * 表单、目录浏览等 modal 局部状态仍由页面持有。
 */
export function useProjects() {
  const [keyword, setKeyword] = useState('')
  const [items, setItems] = useState<Project[]>([])
  const [total, setTotal] = useState(0)
  const [nextPage, setNextPage] = useState(1)
  const [version, setVersion] = useState(0)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState('')
  const request = useRef<AbortController | null>(null)
  const busy = useRef(false)

  const load = useCallback(async (page: number, append: boolean) => {
    if (append && busy.current) return
    busy.current = true
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    if (append) setLoadingMore(true)
    else setLoading(true)
    setError('')
    try {
      const result = await fetchProjects(keyword, page, controller.signal)
      if (controller.signal.aborted) return
      setItems(current => append ? [...current, ...result.items.filter(item => !current.some(existing => existing.id === item.id))] : result.items)
      setTotal(result.total)
      setNextPage(page + 1)
    } catch (err) {
      if (!controller.signal.aborted) setError(err instanceof Error ? err.message : '项目操作失败')
    } finally {
      if (!controller.signal.aborted) { setLoading(false); setLoadingMore(false); busy.current = false }
    }
  }, [keyword])

  useEffect(() => {
    const timer = setTimeout(() => void load(1, false), 250)
    return () => { clearTimeout(timer); request.current?.abort() }
  }, [load, version])

  const refresh = () => setVersion(value => value + 1)
  const loadMore = () => {
    if (loading || loadingMore || error || items.length >= total) return
    void load(nextPage, true)
  }
  // search 保持稳定引用：页面 effect 依赖它清空搜索时不会循环重建。
  const search = useCallback((value: string) => {
    setKeyword(value)
    setItems([])
    setTotal(0)
    setError('')
    setLoading(true)
  }, [])

  return { keyword, setKeyword: search, items, revision: version, loading, loadingMore, error, refresh, loadMore }
}
