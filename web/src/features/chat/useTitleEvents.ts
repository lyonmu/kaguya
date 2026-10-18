import { useEffect, useRef } from 'react'
import { SUCCESS_CODE } from '../../api/codes'
import { isAbortError } from '../../api/errors'
import { ApiRequestError } from '../../api/errors'
import { openStream, readEventStream } from '../../api/stream'
import { isApiEnvelope } from '../../api/transport'
import { titleEventGuard } from './guards'
import type { TitleEvent } from './types'

/** 断线重连的初始/最大退避；成功建立连接后重置。 */
const INITIAL_BACKOFF_MS = 1000
const MAX_BACKOFF_MS = 30_000

/**
 * useTitleEvents 订阅 /v1/events 标题事件流。一个 Provider 只建立一条连接；
 * 每次连接有独立signal；先读事件，再通知Provider补查已加载ID。
 * 取消（卸载/切换）保持安静，不弹错误。
 */
export function useTitleEvents(onEvent: (event: TitleEvent) => void, onReady?: (signal: AbortSignal) => () => void, onError?: () => void) {
  const handler = useRef({ onEvent, onReady, onError })
  handler.current = { onEvent, onReady, onError }

  useEffect(() => {
    let controller: AbortController | undefined
    let disposed = false
    let timer: ReturnType<typeof setTimeout> | undefined
    let backoff = INITIAL_BACKOFF_MS

    const connect = async () => {
      const connection = new AbortController()
      controller = connection
      let cleanup: (() => void) | undefined
      let reading: Promise<void> | undefined
      try {
        const body = await openStream('/v1/events', { method: 'GET', signal: connection.signal })
        if (disposed || connection.signal.aborted) { await body.cancel(); return }
        backoff = INITIAL_BACKOFF_MS
        reading = readEventStream(body, raw => {
          let payload: unknown
          try {
            payload = JSON.parse(raw)
          } catch {
            throw new ApiRequestError('标题事件格式不正确', { kind: 'protocol' })
          }
          if (!isApiEnvelope(payload) || payload.code !== SUCCESS_CODE) throw new ApiRequestError('标题事件格式不正确', { kind: 'protocol' })
          const checked = titleEventGuard(payload.data)
          if (!checked.ok) throw new ApiRequestError('标题事件格式不正确', { kind: 'protocol' })
          if (!disposed && !connection.signal.aborted) handler.current.onEvent(checked.value)
        }, { signal: connection.signal })
        cleanup = handler.current.onReady?.(connection.signal)
        await reading
      } catch (error) {
        // 取消或卸载是正常生命周期；其余断线在下方按退避重连。
        if (disposed || connection.signal.aborted || isAbortError(error)) return
        handler.current.onError?.()
      } finally {
        connection.abort()
        // onReady同步失败时也必须join已经启动的reader，取消rejection不悬空。
        try { cleanup?.() } finally { await reading?.catch(() => {}) }
      }
      if (disposed) return
      timer = setTimeout(() => void connect(), backoff)
      backoff = Math.min(backoff * 2, MAX_BACKOFF_MS)
    }

    void connect()
    return () => {
      disposed = true
      controller?.abort()
      if (timer) clearTimeout(timer)
    }
  }, [])
}
