import { useEffect, useRef } from 'react'
import { SUCCESS_CODE } from '../../api/codes'
import { isAbortError } from '../../api/errors'
import { openStream, readEventStream } from '../../api/stream'
import { isApiEnvelope } from '../../api/transport'
import { titleEventGuard } from './guards'
import type { TitleEvent } from './types'

/** 断线重连的初始/最大退避；成功建立连接后重置。 */
const INITIAL_BACKOFF_MS = 1000
const MAX_BACKOFF_MS = 30_000

/**
 * useTitleEvents 订阅 /v1/events 标题事件流。一个 Provider 只建立一条连接；
 * 断线按有界指数退避重连，重连后由调用方重新读取会话列表补齐快照（DB 是权威来源）。
 * 取消（卸载/切换）保持安静，不弹错误。
 */
export function useTitleEvents(onEvent: (event: TitleEvent) => void) {
  const handler = useRef(onEvent)
  handler.current = onEvent

  useEffect(() => {
    const controller = new AbortController()
    let disposed = false
    let timer: ReturnType<typeof setTimeout> | undefined
    let backoff = INITIAL_BACKOFF_MS

    const connect = async () => {
      try {
        const body = await openStream('/v1/events', { method: 'GET', signal: controller.signal })
        backoff = INITIAL_BACKOFF_MS
        await readEventStream(body, raw => {
          let payload: unknown
          try {
            payload = JSON.parse(raw)
          } catch {
            return
          }
          if (!isApiEnvelope(payload) || payload.code !== SUCCESS_CODE) return
          const checked = titleEventGuard(payload.data)
          if (checked.ok) handler.current(checked.value)
        })
      } catch (error) {
        // 取消或卸载是正常生命周期；其余断线在下方按退避重连。
        if (disposed || controller.signal.aborted || isAbortError(error)) return
      }
      if (disposed || controller.signal.aborted) return
      timer = setTimeout(() => void connect(), backoff)
      backoff = Math.min(backoff * 2, MAX_BACKOFF_MS)
    }

    void connect()
    return () => {
      disposed = true
      controller.abort()
      if (timer) clearTimeout(timer)
    }
  }, [])
}
