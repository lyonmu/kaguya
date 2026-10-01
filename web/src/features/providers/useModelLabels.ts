import { useEffect, useState } from 'react'
import { fetchModelLabels } from './api'
import type { ModelLabelOption } from './types'

/**
 * useModelLabels 缓存不含密钥的模型标签：同一进程共享一次 GET 与结果，
 * 消费者卸载只结束自身等待；最后订阅者离开才中止在途请求。
 * provider/model 写成功后调用 invalidateModelLabels 使缓存失效并重取。
 */
let cached: ModelLabelOption[] | undefined
let inflight: Promise<ModelLabelOption[]> | undefined
let controller: AbortController | undefined
let subscribers = 0
const listeners = new Set<() => void>()

export function invalidateModelLabels() {
  cached = undefined
  controller?.abort()
  controller = undefined
  inflight = undefined
  for (const listener of listeners) listener()
}

function load(): Promise<ModelLabelOption[]> {
  if (cached) return Promise.resolve(cached)
  if (!inflight) {
    controller = new AbortController()
    inflight = fetchModelLabels(controller.signal).then(items => {
      cached = items ?? []
      return cached
    }).finally(() => {
      inflight = undefined
      controller = undefined
    })
  }
  return inflight
}

export function useModelLabels() {
  const [models, setModels] = useState<ModelLabelOption[]>(cached ?? [])
  const [loading, setLoading] = useState(!cached)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)

  useEffect(() => {
    const listener = () => setRevision(value => value + 1)
    listeners.add(listener)
    return () => { listeners.delete(listener) }
  }, [])

  useEffect(() => {
    let active = true
    subscribers++
    if (cached) {
      setModels(cached)
      setError('')
      setLoading(false)
    } else {
      setLoading(true)
      load().then(items => {
        if (!active) return
        setModels(items)
        setError('')
        setLoading(false)
      }).catch(err => {
        if (!active) return
        setError(err instanceof Error ? err.message : '模型加载失败')
        setLoading(false)
      })
    }
    return () => {
      active = false
      subscribers--
      if (subscribers === 0) {
        controller?.abort()
        controller = undefined
        inflight = undefined
      }
    }
  }, [revision])

  return {
    models,
    loading,
    error,
    retry: () => {
      cached = undefined
      setRevision(value => value + 1)
    },
  }
}
