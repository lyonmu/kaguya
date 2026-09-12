import { ApiRequestError, SUCCESS_CODE, isApiEnvelope } from '../../api/http'
import { sseFrameGuard } from './guards'
import type { ChatFrame } from './types'

// Decode lines incrementally: both UTF-8 characters and CRLF can cross chunks.
export async function consumeSSE(
  body: ReadableStream<Uint8Array>,
  onFrame: (frame: ChatFrame) => void,
) {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let data: string[] = []
  let done = false
  const dispatch = () => {
    if (!data.length) return
    const raw = data.join('\n')
    data = []
    let payload: unknown
    try {
      payload = JSON.parse(raw)
    } catch {
      throw new ApiRequestError('无法解析 SSE 事件')
    }
    // 网络数据先作为 unknown 校验 envelope 与帧结构，坏响应不会进入 reducer。
    if (!isApiEnvelope(payload)) {
      throw new ApiRequestError('SSE 事件格式不正确')
    }
    if (payload.code !== SUCCESS_CODE) {
      throw new ApiRequestError(payload.message || '对话生成失败', { code: payload.code })
    }
    const checked = sseFrameGuard(payload.data)
    if (!checked.ok) {
      throw new ApiRequestError(checked.error)
    }
    const frame = checked.value
    if (frame.chat.flag === 'error') {
      throw new ApiRequestError(frame.chat.content || '对话生成失败')
    }
    onFrame(frame)
    done = frame.chat.flag === 'done'
  }
  const line = (value: string) => {
    if (!value) dispatch()
    else if (value === 'data' || value.startsWith('data:')) {
      data.push(value.slice(5).replace(/^ /, ''))
    }
  }
  try {
    while (!done) {
      const result = await reader.read()
      buffer += decoder.decode(result.value, { stream: !result.done })
      let match: RegExpExecArray | null
      while ((match = /\r\n|\r|\n/.exec(buffer))) {
        if (!result.done && match[0] === '\r' && match.index === buffer.length - 1) break
        line(buffer.slice(0, match.index))
        buffer = buffer.slice(match.index + match[0].length)
        if (done) break
      }
      if (result.done) {
        if (!done) throw new ApiRequestError('连接意外中断，未收到对话完成事件；请重新加载历史确认结果')
        break
      }
    }
  } finally {
    try { await reader.cancel() } finally { reader.releaseLock() }
  }
}
