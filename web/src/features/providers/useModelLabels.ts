import { useCallback, useEffect, useState } from 'react'
import { buildUrl } from '../../api/transport'
import { fetchModelLabels } from './api'
import type { ModelLabelOption } from './types'

interface ModelLabelSlot {
  key: string
  generation: number
  controller: AbortController
  promise: Promise<ModelLabelOption[]>
  subscribers: number
  settled: boolean
}

interface ModelLabelState {
  key: string | undefined
  generation: number
  models: ModelLabelOption[]
  loading: boolean
  error: string
}

// 仅保留一个服务的一代非secret标签；旧slot仍归自己的消费者释放。
let key: string | undefined
let generation = 0
let cached: ModelLabelOption[] | undefined
let inflight: ModelLabelSlot | undefined
const listeners = new Set<() => void>()

function apiIdentity(): { key: string | undefined; error: string } {
  try {
    return { key: buildUrl(''), error: '' }
  } catch (error) {
    return { key: undefined, error: error instanceof Error ? error.message : '模型加载失败' }
  }
}

function reset(nextKey: string | undefined, owner?: () => void) {
  generation++
  key = nextKey
  cached = undefined
  const previous = inflight
  inflight = undefined
  previous?.controller.abort()
  for (const listener of listeners) if (listener !== owner) listener()
}

/** provider/model写成功及手动retry共用失效入口，先失效再通知hook。 */
export function invalidateModelLabels() {
  reset(key)
}

function acquire(currentKey: string) {
  if (!inflight || inflight.controller.signal.aborted) {
    const controller = new AbortController()
    const currentGeneration = generation
    const slot: ModelLabelSlot = {
      key: currentKey, generation: currentGeneration, controller, subscribers: 0, settled: false,
      promise: fetchModelLabels(controller.signal).then(items => {
        if (inflight === slot && key === slot.key && generation === slot.generation && !controller.signal.aborted && apiIdentity().key === slot.key) cached = items
        return items
      }).finally(() => {
        slot.settled = true
        if (inflight === slot) inflight = undefined
      }),
    }
    inflight = slot
  }
  const slot = inflight
  slot.subscribers++
  let released = false
  const release = () => {
    if (released) return
    released = true
    slot.subscribers--
    if (slot.subscribers === 0 && !slot.settled) {
      if (inflight === slot) inflight = undefined
      slot.controller.abort()
    }
  }
  return { slot, release }
}

export function useModelLabels() {
  const identity = apiIdentity()
  const currentKey = identity.key
  const [revision, setRevision] = useState(0)
  const listener = useCallback(() => setRevision(value => value + 1), [])
  const [state, setState] = useState<ModelLabelState>(() => ({
    key: currentKey, generation, models: key === currentKey ? cached ?? [] : [],
    loading: currentKey !== undefined && (key !== currentKey || !cached), error: identity.error,
  }))

  useEffect(() => {
    listeners.add(listener)
    return () => { listeners.delete(listener) }
  }, [listener])

  useEffect(() => {
    // API前缀可能在render到effect之间改变，不给旧key发向新服务的请求。
    if (apiIdentity().key !== currentKey) {
      listener()
      return
    }
    if (key !== currentKey) reset(currentKey, listener)
    const currentGeneration = generation
    const initial: ModelLabelState = {
      key: currentKey, generation: currentGeneration, models: [], loading: false, error: identity.error,
    }
    if (currentKey === undefined) {
      setState(initial)
      return
    }
    if (cached) {
      setState({ ...initial, models: cached })
      return
    }
    let active = true
    const { slot, release } = acquire(currentKey)
    const owns = () => active && key === currentKey && generation === currentGeneration && !slot.controller.signal.aborted && apiIdentity().key === currentKey
    setState({ ...initial, loading: true })
    slot.promise.then(models => {
      if (owns()) setState({ ...initial, models })
    }).catch(error => {
      if (owns()) setState({ ...initial, error: error instanceof Error ? error.message : '模型加载失败' })
    })
    return () => { active = false; release() }
  }, [currentKey, identity.error, listener, revision])

  // render即隐藏不同服务或已经失效的结果，不等effect清理旧状态。
  const visible = state.key === currentKey && state.generation === generation
    ? state
    : { models: [], loading: currentKey !== undefined, error: identity.error }
  return { models: visible.models, loading: visible.loading, error: visible.error, retry: invalidateModelLabels }
}
