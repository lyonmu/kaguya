import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchProjectFiles } from '../project/api'
import type { ReferenceCandidate } from '../project/types'

interface ReferencePage {
  key: string
  items: ReferenceCandidate[]
  loading: boolean
  loadingMore: boolean
  error: string
  truncated: boolean
  scanIncomplete: boolean
  nextCursor: string
}

interface ReferenceScope {
  key: string
  controller?: AbortController
  loading: boolean
  nextCursor: string
  consumed: Set<string>
  request: (cursor: string) => void
}

const empty = (key: string): ReferencePage => ({ key, items: [], loading: false, loadingMore: false, error: '', truncated: false, scanIncomplete: false, nextCursor: '' })
const unique = (items: ReferenceCandidate[]) => {
  const seen = new Set<string>()
  return items.filter(item => {
    const key = JSON.stringify([item.type, item.path])
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

/** 150ms去抖；每query/project代一个请求，分页游标不授予正文访问权限。 */
export function useReferences(projectId: string | undefined, query: string | undefined, enabled: boolean) {
  const key = JSON.stringify([projectId, query, enabled])
  const [page, setPage] = useState<ReferencePage>(() => empty(key))
  const active = useRef<ReferenceScope | undefined>(undefined)

  useEffect(() => {
    setPage(empty(key))
    if (!enabled || query === undefined || !projectId) return
    const scope: ReferenceScope = { key, loading: true, nextCursor: '', consumed: new Set(), request: () => {} }
    active.current = scope
    scope.request = cursor => {
      if (active.current !== scope || scope.controller) return
      const controller = new AbortController()
      scope.controller = controller
      scope.loading = true
      const owns = () => active.current === scope && scope.controller === controller && !controller.signal.aborted
      setPage(current => ({ ...current, loading: !cursor, loadingMore: !!cursor, error: '' }))
      fetchProjectFiles(projectId, query, { includeDirectories: true, cursor: cursor || undefined, signal: controller.signal })
        .then(result => {
          if (!owns()) return
          const nextCursor = result.next_cursor ?? ''
          if (nextCursor && (nextCursor === cursor || !result.cursor_reset && scope.consumed.has(nextCursor))) {
            scope.nextCursor = ''
            setPage(current => ({ ...current, nextCursor: '', error: '文件搜索游标未推进，请重新搜索' }))
            return
          }
          if (result.cursor_reset) scope.consumed.clear()
          if (cursor) scope.consumed.add(cursor)
          scope.nextCursor = nextCursor
          const candidates = result.items ?? result.files.map(path => ({ type: 'file' as const, path }))
          setPage(current => ({
            ...current,
            items: unique(cursor && !result.cursor_reset ? [...current.items, ...candidates] : candidates),
            truncated: result.truncated,
            scanIncomplete: result.scan_incomplete === true,
            nextCursor,
            error: '',
          }))
        })
        .catch(err => {
          if (owns()) setPage(current => ({ ...current, error: err instanceof Error ? err.message : '文件搜索失败' }))
        })
        .finally(() => {
          if (!owns()) return
          scope.controller = undefined
          scope.loading = false
          setPage(current => ({ ...current, loading: false, loadingMore: false }))
        })
    }
    setPage(current => ({ ...current, loading: true }))
    const timer = setTimeout(() => scope.request(''), 150)
    return () => {
      clearTimeout(timer)
      scope.controller?.abort()
      if (active.current === scope) active.current = undefined
    }
  }, [key, projectId, query, enabled])

  const loadMore = useCallback(() => {
    const scope = active.current
    if (scope?.key === key && !scope.loading && scope.nextCursor) scope.request(scope.nextCursor)
  }, [key])

  // 切换props后的render立刻隐藏旧代数据，无须等待旧effect cleanup。
  return { ...(page.key === key ? page : empty(key)), loadMore }
}
