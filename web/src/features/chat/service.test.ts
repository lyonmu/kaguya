/// <reference types="node" />
import { afterEach, describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { syncCompletedConversation, syncStartedConversation } from './service'

const originalFetch = globalThis.fetch
afterEach(() => { globalThis.fetch = originalFetch })
const response = (data: unknown) => Response.json({ code: 100000, data })

describe('conversation completion without title waits', () => {
  it('refreshes details and list once without a title POST, including default titles', async () => {
    const calls: string[] = [], updates: string[] = []
    globalThis.fetch = (async (url, init) => {
      assert.ok(init?.signal)
      calls.push(`${init?.method} ${String(url).split('/conversation/')[1]}`)
      return response({ id: '123', title: '新对话' })
    }) as typeof fetch
    await syncCompletedConversation('123', new AbortController().signal, {
      onDetail: detail => updates.push(detail.title), onCompleted: async () => { updates.push('refresh quietly') },
    })
    assert.deepEqual(calls, ['GET 123'])
    assert.deepEqual(updates, ['新对话', 'refresh quietly'])
  })
  it('start only reads necessary detail and repeated default-title turns never generate', async () => {
    const methods: string[] = []
    globalThis.fetch = (async (_url, init) => { methods.push(init?.method ?? ''); return response({ id: '123', title: '新对话' }) }) as typeof fetch
    await syncStartedConversation('123', new AbortController().signal, { onDetail: () => {} })
    for (let i = 0; i < 3; i++) await syncCompletedConversation('123', new AbortController().signal, { onDetail: () => {}, onCompleted: async () => {} })
    assert.deepEqual(methods, ['GET', 'GET', 'GET', 'GET'])
  })
  it('does not regenerate a manual title', async () => {
    let requests = 0
    globalThis.fetch = (async () => { requests++; return response({ id: '123', title: '用户自定义' }) }) as typeof fetch
    await syncCompletedConversation('123', new AbortController().signal, { onDetail: () => {}, onCompleted: async () => {} })
    assert.equal(requests, 1)
  })
  it('propagates detail failure without retry loops or replacing state', async () => {
    let requests = 0
    globalThis.fetch = (async () => { requests++; return Response.json({ code: 500001, message: '详情查询失败' }) }) as typeof fetch
    await assert.rejects(syncCompletedConversation('123', new AbortController().signal, {
      onDetail: () => assert.fail('failed detail applied'), onCompleted: async () => assert.fail('unexpected list refresh'),
    }), /详情查询失败/)
    assert.equal(requests, 1)
  })
  it('ignores a late detail response after cancellation', async () => {
    const controller = new AbortController()
    let release!: (response: Response) => void
    globalThis.fetch = (async () => new Promise<Response>(resolve => { release = resolve })) as typeof fetch
    const task = syncCompletedConversation('123', controller.signal, {
      onDetail: () => assert.fail('stale detail applied'), onCompleted: async () => assert.fail('unexpected list refresh'),
    })
    controller.abort()
    release(response({ id: '123', title: '迟到响应' }))
    await assert.rejects(task, { name: 'AbortError' })
  })
})
