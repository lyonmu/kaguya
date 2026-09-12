/// <reference types="node" />
import { afterEach, describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { streamChat } from './api'

const originalFetch = globalThis.fetch
afterEach(() => { globalThis.fetch = originalFetch })

describe('chat API', () => {
  it('POSTs a continuation ID and consumes SSE', async () => {
    globalThis.fetch = (async (url, init) => {
      assert.ok(String(url).endsWith('/v1/chat/sse'))
      assert.equal(init?.method, 'POST')
      assert.deepEqual(JSON.parse(String(init?.body)), { id: '123', messages: '你好' })
      assert.ok(init?.signal)
      return new Response('data: {"code":100000,"data":{"chat":{"id":"123","flag":"done"}}}\n\n', { headers: { 'Content-Type': 'text/event-stream' } })
    }) as typeof fetch
    let done = false
    await streamChat('123', '你好', new AbortController().signal, () => { done = true })
    assert.equal(done, true)
  })
  it('surfaces JSON validation failures before SSE starts', async () => {
    globalThis.fetch = (async () => Response.json({ code: 400001, message: '参数错误' })) as typeof fetch
    await assert.rejects(streamChat('', '你好', new AbortController().signal, () => {}), /参数错误/)
  })
  it('sends selected project files independently from the visible message', async () => {
    globalThis.fetch = (async (_url, init) => {
      assert.deepEqual(JSON.parse(String(init?.body)), { id: '123', messages: '检查实现', model_id: 'm', project_id: 'p', files: ['docs/my file.md'] })
      return new Response('data: {"code":100000,"data":{"chat":{"id":"123","flag":"done"}}}\n\n', { headers: { 'Content-Type': 'text/event-stream' } })
    }) as typeof fetch
    await streamChat('123', '检查实现', new AbortController().signal, () => {}, 'm', 'p', ['docs/my file.md'])
  })
  it('propagates cancellation without retrying the POST', async () => {
    let calls = 0
    globalThis.fetch = (async (_url, init) => {
      calls++
      init?.signal?.throwIfAborted()
      throw new Error('expected an aborted signal')
    }) as typeof fetch
    const controller = new AbortController()
    controller.abort()
    await assert.rejects(streamChat('', '你好', controller.signal, () => {}), { name: 'AbortError' })
    assert.equal(calls, 1)
  })
})
