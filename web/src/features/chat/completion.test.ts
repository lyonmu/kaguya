/// <reference types="node" />
import { afterEach, describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { syncCompletedConversation } from './completion'
import type { ConversationTitle } from './types'

const originalFetch = globalThis.fetch
afterEach(() => { globalThis.fetch = originalFetch })
const response = (data: unknown) => Response.json({ code: 100000, data })

describe('conversation completion', () => {
  it('refreshes details/list once, then patches the title after one POST', async () => {
    const calls: string[] = []
    globalThis.fetch = (async (url, init) => {
      assert.ok(init?.signal)
      calls.push(`${init?.method} ${String(url).split('/conversation/')[1]}`)
      return response(String(url).endsWith('/title/wait') ? { id: '123', title: '自动标题' } : { id: '123', title: '新对话' })
    }) as typeof fetch
    const updates: string[] = []
    await syncCompletedConversation('123', new AbortController().signal, {
      onDetail: detail => updates.push(detail.title),
      onCompleted: async () => { updates.push('refresh quietly') },
      onTitle: title => updates.push(title.title),
    })
    assert.deepEqual(calls, ['GET 123', 'POST 123/title/wait'])
    assert.deepEqual(updates, ['新对话', 'refresh quietly', '自动标题'])
  })

  it('retries a default title once on the next turn, then stops after success', async () => {
    let storedTitle = '新对话'
    let generations = 0
    globalThis.fetch = (async url => {
      if (String(url).endsWith('/title/wait')) {
        generations++
        if (generations === 2) storedTitle = '生成成功'
      }
      return response({ id: '123', title: storedTitle, turn_count: 5 })
    }) as typeof fetch
    const titles: string[] = []
    const complete = () => syncCompletedConversation('123', new AbortController().signal, {
      onDetail: () => {}, onCompleted: async () => {}, onTitle: title => titles.push(title.title),
    })
    await complete()
    assert.equal(generations, 1)
    await complete()
    assert.equal(generations, 2)
    await complete()
    assert.equal(generations, 2)
    assert.deepEqual(titles, ['新对话', '生成成功'])
  })

  it('does not regenerate a manual title', async () => {
    let requests = 0
    globalThis.fetch = (async () => { requests++; return response({ id: '123', title: '用户自定义' }) }) as typeof fetch
    await syncCompletedConversation('123', new AbortController().signal, {
      onDetail: () => {}, onCompleted: async () => {}, onTitle: () => assert.fail('unexpected title update'),
    })
    assert.equal(requests, 1)
  })

  it('does not loop on a failed request or replace the existing title', async () => {
    let requests = 0
    let title = '新对话'
    globalThis.fetch = (async () => {
      requests++
      return requests === 1 ? response({ id: '123', title }) : Response.json({ code: 500001, message: '标题查询失败' })
    }) as typeof fetch
    await assert.rejects(syncCompletedConversation('123', new AbortController().signal, {
      onDetail: () => {}, onCompleted: async () => {}, onTitle: result => { title = result.title },
    }), /标题查询失败/)
    assert.equal(requests, 2)
    assert.equal(title, '新对话')
  })

  it('ignores a late title response after switching conversation or manually renaming', async () => {
    const controller = new AbortController()
    let release!: (response: Response) => void
    let started!: () => void
    const waiting = new Promise<void>(resolve => { started = resolve })
    globalThis.fetch = (async url => {
      if (String(url).endsWith('/title/wait')) {
        started()
        return new Promise<Response>(resolve => { release = resolve })
      }
      return response({ id: '123', title: '新对话' })
    }) as typeof fetch
    const titles: ConversationTitle[] = []
    const task = syncCompletedConversation('123', controller.signal, {
      onDetail: () => {}, onCompleted: async () => {}, onTitle: title => titles.push(title),
    })
    await waiting
    controller.abort()
    release(response({ id: '123', title: '过期标题' }))
    await assert.rejects(task, { name: 'AbortError' })
    assert.deepEqual(titles, [])
  })
})
