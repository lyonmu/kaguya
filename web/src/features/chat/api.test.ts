/// <reference types="node" />
import { afterEach, describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { fetchBlock, fetchTurnPage, streamChat, updateConversation } from './api'
import { isBlock } from './guards'

const originalFetch = globalThis.fetch
afterEach(() => { globalThis.fetch = originalFetch })

describe('chat API', () => {
  it('validates phase in historical blocks without narrowing legacy or extension fields', async () => {
    for (const phase of [undefined, 'start', 'delta', 'block_end', null, 'future', 1, false, {}]) {
      const block = { type: 'text', phase, future_extension: true }
      const accepted = phase === undefined || ['start', 'delta', 'block_end'].includes(phase as string)
      assert.equal(isBlock(block), accepted)
      globalThis.fetch = (async url => Response.json({ code: 100000, data: String(url).includes('/blocks/') ? block : {
        items: [{ turn_index: 1, user_content: 'q', started_at: '2026-01-01T00:00:00Z', blocks: [block] }], page: 1, total_pages: 1,
      } })) as typeof fetch
      for (const request of [() => fetchBlock('1', 1, 1), () => fetchTurnPage('1', 1)]) {
        if (accepted) await request()
        else await assert.rejects(request(), /格式不正确/)
      }
    }
    assert.equal(isBlock({ type: 'text', extra: 1 }), true)
  })
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

it('updates the per-conversation memory mode', async () => {
  let payload: Record<string, unknown> | undefined
  globalThis.fetch = (async (url, init) => {
    assert.ok(String(url).endsWith('/v1/chat/conversation/c-1'))
    assert.equal(init?.method, 'PUT')
    payload = JSON.parse(String(init.body))
    return Response.json({ code: 100000, data: { id: 'c-1', title: 't', is_project: false, favorite: false, memory_mode: String(payload?.memory_mode ?? 'inherit') } })
  }) as typeof fetch
  const result = await updateConversation('c-1', { memory_mode: 'readonly' })
  assert.equal(payload?.memory_mode, 'readonly')
  assert.equal(result.memory_mode, 'readonly')
})
