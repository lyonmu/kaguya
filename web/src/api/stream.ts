import { ApiRequestError, transportFailure } from './errors'
import { buildUrl, parseEnvelope } from './transport'

/** 单帧上限，避免坏对端无限填充 data 行；正常聊天帧远小于该值。 */
const MAX_FRAME_BYTES = 64 * 1024 * 1024

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
}

/**
 * readEventStream 只做通用 SSE framing：UTF-8/CRLF 跨 chunk、多 data 行、
 * comment 心跳行、单帧上限。业务语义（envelope、done、error）由各 feature 校验。
 */
export async function readEventStream(
  body: ReadableStream<Uint8Array>,
  onData: (raw: string) => void,
  options?: EventStreamOptions,
): Promise<void> {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let data: string[] = []
  let frameBytes = 0
  const stopped = () => options?.shouldStop?.() === true

  const dispatch = () => {
    if (!data.length) return
    const raw = data.join('\n')
    data = []
    frameBytes = 0
    onData(raw)
  }

  const line = (value: string) => {
    if (!value) {
      dispatch()
      return
    }
    if (value.startsWith(':')) return // 心跳/注释
    if (value !== 'data' && !value.startsWith('data:')) return
    const chunk = value.slice(5).replace(/^ /, '')
    frameBytes += chunk.length
    if (frameBytes > MAX_FRAME_BYTES) {
      throw new ApiRequestError('事件帧超过大小上限', { kind: 'protocol' })
    }
    data.push(chunk)
  }

  try {
    while (!stopped()) {
      const result = await reader.read()
      buffer += decoder.decode(result.value, { stream: !result.done })
      let match: RegExpExecArray | null
      while ((match = /\r\n|\r|\n/.exec(buffer))) {
        if (!result.done && match[0] === '\r' && match.index === buffer.length - 1) break
        line(buffer.slice(0, match.index))
        buffer = buffer.slice(match.index + match[0].length)
        if (stopped()) break
      }
      if (result.done) break
    }
  } finally {
    try {
      await reader.cancel()
    } finally {
      reader.releaseLock()
    }
  }
}
