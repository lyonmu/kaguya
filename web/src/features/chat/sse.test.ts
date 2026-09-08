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
