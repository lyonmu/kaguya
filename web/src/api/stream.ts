import { ApiRequestError, isAbortError, transportFailure } from './errors'
import { buildUrl, parseEnvelope } from './transport'
import { EventStreamParser } from './streamParser'

export interface StreamOptions {
  method?: 'GET' | 'POST'
  body?: unknown
  signal?: AbortSignal
}

/** openStream 建立 SSE 连接。非 2xx 或非事件流响应按统一 envelope 抛错。 */
export async function openStream(path: string, options?: StreamOptions): Promise<ReadableStream<Uint8Array>> {
  let response: Response
  try {
    response = await fetch(buildUrl(path), {
      method: options?.method ?? 'POST',
      headers: {
        Accept: 'text/event-stream',
        ...(options?.body === undefined ? {} : { 'Content-Type': 'application/json' }),
      },
      body: options?.body === undefined ? undefined : JSON.stringify(options.body),
      credentials: 'same-origin',
      signal: options?.signal,
    })
  } catch (error) {
    transportFailure(error, options?.signal)
  }

  const isEventStream = response.headers.get('content-type')?.includes('text/event-stream') ?? false
  if (!response.ok || !isEventStream) {
    // parseEnvelope 对坏 envelope/非 2xx 抛出分类错误；成功码但非事件流则明确报协议错误。
    await parseEnvelope(response)
    throw new ApiRequestError('服务端未返回事件流', { status: response.status, kind: 'protocol' })
  }
  if (!response.body) {
    throw new ApiRequestError('浏览器不支持流式响应', { kind: 'protocol' })
  }
  return response.body
}

export interface EventStreamOptions {
  /** 返回 true 时停止读取并取消 reader，用于聊天在 done 帧后结束。 */
  shouldStop?: () => boolean
  /** 标题连接取消时终止等待中的reader，测试流也遵循真实fetch取消语义。 */
  signal?: AbortSignal
}

/**
 * readEventStream 只做通用SSE framing：解码前wire预算、严格UTF-8、跨chunk
 * CRLF与完整事件；业务语义（envelope、done、error）由各feature校验。
 */
export async function readEventStream(
  body: ReadableStream<Uint8Array>,
  onData: (raw: string) => void,
  options?: EventStreamOptions,
): Promise<void> {
  const reader = body.getReader()
  const parser = new EventStreamParser(onData)
  let canceling: Promise<void> | undefined
  const cancel = () => { canceling ??= reader.cancel(); return canceling }
  const abort = () => { void cancel().catch(() => {}) }
  const stopped = () => options?.shouldStop?.() === true
  const checkAbort = () => { if (options?.signal?.aborted) throw new DOMException('事件流已取消', 'AbortError') }
  let failed = false
  let failure: unknown

  try {
    options?.signal?.addEventListener('abort', abort, { once: true })
    if (options?.signal?.aborted) abort()
    checkAbort()
    while (!stopped()) {
      let result: ReadableStreamReadResult<Uint8Array>
      try { result = await reader.read() } catch (error) {
        checkAbort()
        if (isAbortError(error)) throw error
        throw new ApiRequestError('事件流读取失败', { kind: 'network' })
      }
      checkAbort()
      if (result.done) {
        parser.finish(stopped)
        break
      }
      parser.push(result.value, () => { checkAbort(); return stopped() })
    }
  } catch (error) {
    failed = true
    failure = error
  } finally {
    options?.signal?.removeEventListener('abort', abort)
    parser.clear()
    try { await cancel() } catch {
      if (!failed) { failed = true; failure = new ApiRequestError('事件流关闭失败', { kind: 'network' }) }
    }
    try { reader.releaseLock() } catch {
      if (!failed) { failed = true; failure = new ApiRequestError('事件流关闭失败', { kind: 'network' }) }
    }
  }
  if (failed) throw failure
}
