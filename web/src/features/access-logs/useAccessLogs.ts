import { useEffect, useState } from 'react'
import { fetchAccessLogs } from './api'
import type { AccessLogPageData, AccessLogQuery } from './types'

export const DEFAULT_ACCESS_LOG_QUERY: AccessLogQuery = {
  page: 1,
  pageSize: 20,
}

const EMPTY_PAGE: AccessLogPageData = {
  total: 0,
  items: [],
  page: DEFAULT_ACCESS_LOG_QUERY.page,
  pageSize: DEFAULT_ACCESS_LOG_QUERY.pageSize,
}

export function useAccessLogs() {
  const [query, setQuery] = useState<AccessLogQuery>(DEFAULT_ACCESS_LOG_QUERY)
  const [data, setData] = useState<AccessLogPageData>(EMPTY_PAGE)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string>()
  const [reloadKey, setReloadKey] = useState(0)

  useEffect(() => {
    const controller = new AbortController()

    async function loadAccessLogs() {
      setLoading(true)
      setError(undefined)

      try {
        const nextData = await fetchAccessLogs(query, controller.signal)
        setData(nextData)
      } catch (requestError) {
        if (controller.signal.aborted) {
          return
        }

        setError(
          requestError instanceof Error
            ? requestError.message
            : '访问日志加载失败，请稍后重试',
        )
      } finally {
        if (!controller.signal.aborted) {
          setLoading(false)
        }
      }
    }

    void loadAccessLogs()

    return () => controller.abort()
  }, [query, reloadKey])

  return {
    data,
    error,
    loading,
    query,
    reload: () => setReloadKey((key) => key + 1),
    setQuery,
  }
}
