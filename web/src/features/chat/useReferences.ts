import { useEffect, useState } from 'react'
import { fetchProjectFiles } from '../project/api'
import type { ReferenceCandidate } from '../project/types'

/**
 * useReferences 管理 @ 引用的候选搜索：150ms 去抖、stale query 取消、
 * 目录与文件统一候选。它只读取元信息，不读取文件正文。
 */
export function useReferences(projectId: string | undefined, query: string | undefined, enabled: boolean) {
  const [items, setItems] = useState<ReferenceCandidate[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [truncated, setTruncated] = useState(false)

  useEffect(() => {
    setItems([])
    setError('')
    setTruncated(false)
    if (!enabled || query === undefined || !projectId) {
      setLoading(false)
      return
    }
    const controller = new AbortController()
    setLoading(true)
    const timer = setTimeout(() => {
      fetchProjectFiles(projectId, query, { includeDirectories: true, signal: controller.signal })
        .then(page => {
          if (controller.signal.aborted) return
          const candidates = page.items ?? page.files.map(path => ({ type: 'file' as const, path }))
          setItems(candidates)
          setTruncated(page.truncated)
        })
        .catch(err => {
          if (!controller.signal.aborted) setError(err instanceof Error ? err.message : '文件搜索失败')
        })
        .finally(() => {
          if (!controller.signal.aborted) setLoading(false)
        })
    }, 150)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [projectId, query, enabled])

  return { items, loading, error, truncated }
}
