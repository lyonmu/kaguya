/// <reference types="node" />
import { after, afterEach, describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { Window } from 'happy-dom'
import { useChat } from './useChat'
import { useConversations } from './useConversations'

const dom = new Window({ url: 'http://localhost' })
const globals = { window: dom, document: dom.document, navigator: dom.navigator, HTMLElement: dom.HTMLElement, MutationObserver: dom.MutationObserver, IS_REACT_ACT_ENVIRONMENT: true }
const previous = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
const { renderHook, act, cleanup, waitFor } = await import('@testing-library/react')
const originalFetch = globalThis.fetch
const response = (data: unknown) => Response.json({ code: 100000, data })
const detail = { id: '123', title: '已有标题', turn_count: 1 }
const turn = { turn_index: 1, user_content: '你好', blocks: [], started_at: new Date().toISOString() }
afterEach(() => { cleanup(); globalThis.fetch = originalFetch })
after(() => {
  for (const [key, descriptor] of previous) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor)
    else Reflect.deleteProperty(globalThis, key)
  }
  void dom.happyDOM.close()
})
const onCompleted = async () => {}
const onTitle = () => {}

describe('chat refresh stability', () => {
  it('replaces message pages and continues from the latest saved turn', async () => {
    const requested: number[] = []
    globalThis.fetch = (async url => {
      const path = new URL(String(url), 'http://localhost')
      if (path.pathname.endsWith('/turns')) {
        const page = Math.min(Number(path.searchParams.get('page')), 3)
        requested.push(page)
        return response({ items: [{ ...turn, turn_index: page === 3 ? 41 : 1 }], page, total: 41, total_pages: 3, page_size: 20 })
      }
      if (path.pathname.endsWith('/sse')) return new Response(`data: ${JSON.stringify({ code: 100000, data: { chat: { id: '123', flag: 'done' }, usage: { total_tokens: 1 } } })}\n\n`, { headers: { 'Content-Type': 'text/event-stream' } })
      return response({ ...detail, turn_count: 41 })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    assert.equal(result.current.page, 3)
    await act(async () => { await result.current.goToPage(1) })
    assert.equal(result.current.page, 1)
    assert.deepEqual(result.current.turns.map(turn => turn.turn_index), [1])
    await act(async () => { await result.current.send('续聊') })
    assert.deepEqual(requested, [3, 1, 3])
    assert.equal(result.current.page, 3)
    assert.deepEqual(result.current.turns.map(turn => turn.turn_index), [41, 42])
  })

  it('ignores an old page response after selecting another conversation', async () => {
    let release!: (value: Response) => void
    const history = { items: [turn], page: 2, total: 21, total_pages: 2, page_size: 20 }
    globalThis.fetch = (async url => {
      const path = new URL(String(url), 'http://localhost')
      if (path.pathname.endsWith('/123/turns') && path.searchParams.get('page') === '1') return new Promise<Response>(resolve => { release = resolve })
      if (path.pathname.endsWith('/turns')) return response(history)
      return response({ ...detail, id: path.pathname.endsWith('/456') ? '456' : '123', turn_count: 21 })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    let pending!: Promise<void>
    act(() => { pending = result.current.goToPage(1) })
    await act(async () => { await result.current.select('456') })
    await act(async () => { release(response({ ...history, page: 1, items: [] })); await pending })
    assert.equal(result.current.id, '456')
    assert.equal(result.current.page, 2)
    assert.equal(result.current.turns.length, 1)
    assert.equal(result.current.loading, false)
  })

  it('appends conversation pages without pagination controls and resets filters', async () => {
    globalThis.fetch = (async url => {
      const path = new URL(String(url), 'http://localhost')
      const page = Number(path.searchParams.get('page'))
      return response({ items: [{ ...detail, id: String(page), title: path.searchParams.get('keyword') || '标题' }], total: 2 })
    }) as typeof fetch
    const { result } = renderHook(() => useConversations())
    await waitFor(() => assert.equal(result.current.items.length, 1))
    act(() => { result.current.loadMore(); result.current.loadMore() })
    await waitFor(() => assert.equal(result.current.items.length, 2))
    assert.deepEqual(result.current.items.map(item => item.id), ['1', '2'])
    act(() => { result.current.search('新筛选') })
    assert.equal(result.current.items.length, 0)
    await waitFor(() => assert.equal(result.current.items[0]?.title, '新筛选'))
    assert.equal(result.current.items.length, 1)
  })

  it('keeps current title, messages and view identity while reloading history', async () => {
    let delayed = false
    const releases: Array<() => void> = []
    globalThis.fetch = (async url => {
      const data = String(url).includes('/turns') ? { items: [turn], has_more: false, next_before: 0, page: 1, total_pages: 1, total: 1, page_size: 20 } : detail
      if (delayed) return new Promise<Response>(resolve => releases.push(() => resolve(response(data))))
      return response(data)
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    const oldTurns = result.current.turns
    const oldConversation = result.current.conversation
    const key = result.current.viewKey
    delayed = true
    let reload!: Promise<void>
    act(() => { reload = result.current.select('123') })
    assert.equal(result.current.loading, true)
    assert.equal(result.current.turns, oldTurns)
    assert.equal(result.current.conversation, oldConversation)
    assert.equal(result.current.viewKey, key)
    await act(async () => { releases.forEach(release => release()); await reload })
    assert.equal(result.current.viewKey, key)
    assert.equal(result.current.loading, false)
  })

  it('does not remount the message view when the first SSE frame assigns an ID', async () => {
    globalThis.fetch = (async (url, init) => {
      if (String(url).endsWith('/sse')) {
        assert.equal(JSON.parse(String(init?.body)).model_id, 'local-model-record-id')
        return new Response(['start', 'done'].map(flag => `data: ${JSON.stringify({ code: 100000, data: { chat: { id: '123', flag }, usage: { total_tokens: 0 } } })}\n\n`).join(''), { headers: { 'Content-Type': 'text/event-stream' } })
      }
      return response(detail)
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    const key = result.current.viewKey
    await act(async () => { await result.current.send('你好', 'local-model-record-id') })
    await waitFor(() => assert.equal(result.current.conversation?.title, '已有标题'))
    assert.equal(result.current.id, '123')
    assert.equal(result.current.viewKey, key)
    assert.equal(result.current.turns[0].user_content, '你好')
    await act(async () => { await result.current.select('') })
    assert.notEqual(result.current.viewKey, key)
    assert.deepEqual(result.current.turns, [])
  })

  it('quiet refresh and title patch retain the list without loading or empty frames', async () => {
    let release!: (value: Response) => void
    let delayed = false
    globalThis.fetch = (async () => delayed
      ? new Promise<Response>(resolve => { release = resolve })
      : response({ items: [detail], total: 1 })) as typeof fetch
    const { result } = renderHook(() => useConversations())
    await waitFor(() => assert.equal(result.current.items.length, 1))
    const items = result.current.items
    delayed = true
    let refresh!: Promise<void>
    act(() => { refresh = result.current.refreshQuietly() })
    assert.equal(result.current.loading, false)
    assert.equal(result.current.items, items)
    act(() => { result.current.updateTitle({ id: '123', title: '新生成标题' }) })
    assert.equal(result.current.loading, false)
    assert.equal(result.current.items[0].title, '新生成标题')
    await act(async () => { release(response({ items: [detail], total: 1 })); await refresh })
    assert.equal(result.current.items[0].title, '新生成标题')
    assert.equal(result.current.loading, false)
  })
})
