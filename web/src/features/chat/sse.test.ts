/// <reference types="node" />
import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { consumeSSE } from './sse'
import type { ChatFrame } from './types'

function frame(flag: 'start' | 'delta' | 'done', content?: string) {
  return { code: 100000, data: { chat: { id: '123', flag, content } } }
}
function stream(text: string, bytewise = false) {
  const bytes = new TextEncoder().encode(text)
  return new ReadableStream<Uint8Array>({ start(controller) {
    if (bytewise) for (const byte of bytes) controller.enqueue(new Uint8Array([byte]))
    else controller.enqueue(bytes)
    controller.close()
  } })
}

describe('SSE transport', () => {
  it('handles split UTF-8, CRLF, comments and multiple events', async () => {
    const received: ChatFrame[] = []
    const text = ': heartbeat\r\n\r\n' + [frame('start'), frame('delta', '你好🌙'), frame('done')].map(value => `data: ${JSON.stringify(value)}\r\n\r\n`).join('')
    await consumeSSE(stream(text, true), value => received.push(value))
    assert.deepEqual(received.map(value => value.chat.flag), ['start', 'delta', 'done'])
    assert.equal(received[1].chat.content, '你好🌙')
  })
  it('joins multiline data and supports CR separators', async () => {
    const text = 'event: message\rdata: {"code":100000,\rdata: "data":{"chat":{"id":"1","flag":"done"}}}\r\r'
    let received = false
    await consumeSSE(stream(text, true), () => { received = true })
    assert.equal(received, true)
  })
  it('rejects API failures and malformed events', async () => {
    await assert.rejects(consumeSSE(stream('data: {"code":500,"message":"模型不可用"}\n\n'), () => {}), /模型不可用/)
    await assert.rejects(consumeSSE(stream('data: not-json\n\n'), () => {}), /无法解析/)
    await assert.rejects(consumeSSE(stream('data: {"code":100000}\n\n'), () => {}), /格式不正确/)
  })
  it('does not silently mark a disconnected stream complete', async () => {
    await assert.rejects(consumeSSE(stream(`data: ${JSON.stringify(frame('start'))}\n\n`), () => {}), /连接意外中断/)
  })
  it('stops at done even when the connection remains open', async () => {
    let cancelled = false
    const body = new ReadableStream<Uint8Array>({
      start(controller) { controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(frame('done'))}\n\n`)) },
      cancel() { cancelled = true },
    })
    await consumeSSE(body, () => {})
    assert.equal(cancelled, true)
  })
})

describe('SSE runtime validation', () => {
  const envelope = (data: unknown) => `data: ${JSON.stringify({ code: 100000, data })}\n\n`
  it('rejects wrong types and keeps accepting valid extra fields', async () => {
    // 非字符串 delta 内容必须拒绝，而不是进入 reducer。
    await assert.rejects(consumeSSE(stream(envelope({ chat: { id: '1', flag: 'delta', content: 42 } })), () => {}), /格式不正确/)
    // 缺失 chat 或未知 flag 同样拒绝。
    await assert.rejects(consumeSSE(stream(envelope({ chat: { id: '1', flag: 'unknown' } })), () => {}), /格式不正确/)
    await assert.rejects(consumeSSE(stream(envelope({ result: 1 })), () => {}), /格式不正确/)
    // usage 字段出现时必须是数字。
    await assert.rejects(consumeSSE(stream(envelope({ chat: { id: '1', flag: 'done' }, usage: { total_tokens: 'x' } })), () => {}), /格式不正确/)
    // 合法额外字段与可选字段继续可用（done 帧结束流）。
    const received: ChatFrame[] = []
    const valid = `data: ${JSON.stringify({ code: 100000, data: { chat: { id: '1', flag: 'delta', content: 'ok' }, usage: { input_tokens: 1, output_tokens: 2, total_tokens: 3 }, extra: { future: true } }, message: 'ok', extra: 1 })}\n\n`
    await consumeSSE(stream(valid + envelope({ chat: { id: '1', flag: 'done' }, usage: { total_tokens: 3 } })), value => received.push(value))
    assert.equal(received.length, 2)
    assert.equal(received[0].chat.content, 'ok')
  })
  it('rejects an error frame without a chat id only when the field is missing', async () => {
    await assert.rejects(consumeSSE(stream(envelope({ chat: { flag: 'error' } })), () => {}), /格式不正确/)
    // 会话尚未建立时后端允许空 id。
    await assert.rejects(consumeSSE(stream(envelope({ chat: { id: '', flag: 'error', content: '模型不可用' } })), () => {}), /模型不可用/)
  })
})
