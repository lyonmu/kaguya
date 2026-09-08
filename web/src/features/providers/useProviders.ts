import { useCallback, useEffect, useState } from 'react'
import { fetchProviders } from './api'
import type { ProviderPageResponse, ProviderQuery } from './types'

export const DEFAULT_PROVIDER_QUERY: ProviderQuery = {
  page: 1,
  pageSize: 20,
}

const EMPTY_PAGE: ProviderPageResponse = {
  total: 0,
  items: [],
  page: 1,
  page_size: 20,
}

export function useProviders() {
  const [query, setQuery] = useState<ProviderQuery>(DEFAULT_PROVIDER_QUERY)
  const [data, setData] = useState<ProviderPageResponse>(EMPTY_PAGE)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string>()
  const [reloadKey, setReloadKey] = useState(0)

  const reload = useCallback(() => setReloadKey((key) => key + 1), [])

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError(undefined)

    fetchProviders(query, controller.signal)
      .then((response) => {
        setData({
          total: response?.total ?? 0,
          items: response?.items ?? [],
          page: response?.page ?? query.page,
          page_size: response?.page_size ?? query.pageSize,
        })
      })
      .catch((requestError: unknown) => {
        if (!controller.signal.aborted) {
          setError(
            requestError instanceof Error
              ? requestError.message
              : 'AI 提供商加载失败，请稍后重试',
          )
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })

    return () => controller.abort()
  }, [query, reloadKey])

  return { data, error, loading, query, reload, setQuery }
}
