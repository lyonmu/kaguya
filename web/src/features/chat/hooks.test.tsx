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
  it('resets pagination and ignores stale results when switching projects', async () => {
    let release!: (value: Response) => void
    globalThis.fetch = (async url => {
      const parsed = new URL(String(url), 'http://localhost')
      const id = parsed.searchParams.get('project_id')
      if (id === 'a') return new Promise<Response>(resolve => { release = resolve })
      assert.equal(parsed.searchParams.get('page'), '1')
      return response({ items: [{ ...detail, id: 'b-chat', project_id: 'b' }], total: 1 })
    }) as typeof fetch
    const { result, rerender } = renderHook(({ id }) => useConversations(id), { initialProps: { id: 'a' } })
    await waitFor(() => assert.ok(release))
    rerender({ id: 'b' })
    await waitFor(() => assert.equal(result.current.items[0]?.id, 'b-chat'))
    await act(async () => { release(response({ items: [detail], total: 1 })); await new Promise(resolve => setTimeout(resolve, 0)) })
    assert.deepEqual(result.current.items.map(item => item.id), ['b-chat'])
  })

  it('replaces message pages and continues from the latest saved turn', async () => {
    const requested: number[] = []
    globalThis.fetch = (async url => {
      const path = new URL(String(url), 'http://localhost')
      if (path.pathname.endsWith('/turns')) {
        assert.equal(path.searchParams.get('limit'), '5')
        assert.equal(path.searchParams.get('compact'), 'true')
        const page = Math.min(Number(path.searchParams.get('page')), 3)
        requested.push(page)
        return response({ items: [{ ...turn, turn_index: page === 3 ? 11 : 1 }], page, total: 11, total_pages: 3, page_size: 5 })
      }
      if (path.pathname.endsWith('/sse')) return new Response(`data: ${JSON.stringify({ code: 100000, data: { chat: { id: '123', flag: 'done' }, usage: { total_tokens: 1 } } })}\n\n`, { headers: { 'Content-Type': 'text/event-stream' } })
      return response({ ...detail, turn_count: 11 })
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
    assert.deepEqual(result.current.turns.map(turn => turn.turn_index), [11, 12])
  })

  it('ignores an old page response after selecting another conversation', async () => {
    let release!: (value: Response) => void
    const history = { items: [turn], page: 2, total: 6, total_pages: 2, page_size: 5 }
    globalThis.fetch = (async url => {
      const path = new URL(String(url), 'http://localhost')
      if (path.pathname.endsWith('/123/turns') && path.searchParams.get('page') === '1') return new Promise<Response>(resolve => { release = resolve })
      if (path.pathname.endsWith('/turns')) return response(history)
      return response({ ...detail, id: path.pathname.endsWith('/456') ? '456' : '123', turn_count: 6 })
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
      const data = String(url).includes('/turns') ? { items: [turn], has_more: false, next_before: 0, page: 1, total_pages: 1, total: 1, page_size: 5 } : detail
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

describe('independent conversation streams', () => {
  it('keeps interleaved streams isolated before IDs arrive and cancels only the selected one', async () => {
    const streams: Array<{ emit: (id: string, flag: string, text?: string) => void; signal: AbortSignal }> = []
    globalThis.fetch = (async (url, init) => {
      if (!String(url).endsWith('/sse')) return response({ ...detail, id: String(url).split('/').at(-1) })
      const signal = init!.signal as AbortSignal
      return new Response(new ReadableStream({ start(controller) {
        streams.push({ signal, emit(id, flag, text) {
          controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ code: 100000, data: { chat: { id, flag, content: text }, usage: { total_tokens: 1 } } })}\n\n`))
          if (flag === 'done') controller.close()
        } })
        signal.addEventListener('abort', () => controller.error(new DOMException('Aborted', 'AbortError')))
      } }), { headers: { 'Content-Type': 'text/event-stream' } })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    let first!: Promise<void>
    act(() => { first = result.current.send('first', 'm', 'project-a') })
    const firstKey = result.current.sessionKey
    assert.equal(result.current.localSessions.length, 1)
    await act(async () => { await result.current.select('') })
    let second!: Promise<void>
    act(() => { second = result.current.send('second') })
    // Duplicate submits in the same conversation do not launch another request.
    await act(async () => { await result.current.send('duplicate') })
    assert.equal(streams.length, 2)
    await act(async () => {
      streams[0].emit('111', 'start')
      streams[1].emit('222', 'start')
      streams[0].emit('111', 'delta', 'answer one')
      streams[1].emit('222', 'delta', 'answer two')
    })
    assert.equal(result.current.id, '222')
    assert.equal(result.current.turns[0].blocks[0].text, 'answer two')
    await act(async () => { await result.current.select(firstKey) })
    assert.equal(result.current.id, '111')
    assert.equal(result.current.turns[0].blocks[0].text, 'answer one')
    assert.equal(result.current.localSessions.find(item => item.key === firstKey)?.projectId, 'project-a')
    await act(async () => { result.current.stop(); await first })
    assert.equal(streams[0].signal.aborted, true)
    assert.equal(streams[1].signal.aborted, false)
    assert.equal(result.current.turns[0].status, 'stopped')
    await act(async () => { streams[1].emit('222', 'done'); await second })
    assert.equal(result.current.id, '111')
    assert.equal(result.current.turns[0].status, 'stopped')
    await act(async () => { await result.current.select('222') })
    assert.equal(result.current.turns[0].status, 'done')
    assert.equal(result.current.turns[0].blocks[0].text, 'answer two')
    await waitFor(() => assert.equal(result.current.conversation?.id, '222'))
  })

  it('aborts every outstanding stream when the app unmounts', async () => {
    const signals: AbortSignal[] = []
    globalThis.fetch = (async (_url, init) => {
      const signal = init!.signal as AbortSignal
      signals.push(signal)
      return new Response(new ReadableStream({ start(controller) {
        signal.addEventListener('abort', () => controller.error(new DOMException('Aborted', 'AbortError')))
      } }), { headers: { 'Content-Type': 'text/event-stream' } })
    }) as typeof fetch
    const { result, unmount } = renderHook(() => useChat(onCompleted, onTitle))
    let a!: Promise<void>, b!: Promise<void>
    act(() => { a = result.current.send('a') })
    await act(async () => { await result.current.select('') })
    act(() => { b = result.current.send('b') })
    unmount()
    await Promise.all([a, b])
    assert.equal(signals.length, 2)
    assert.ok(signals.every(signal => signal.aborted))
  })
})

it('keeps drafts and model choices per conversation and reuses the saved turn index after failure', async () => {
  let fail = true
  globalThis.fetch = (async url => {
    if (!String(url).endsWith('/sse')) return response(detail)
    if (fail) throw new Error('provider unavailable')
    return new Response(`data: ${JSON.stringify({ code: 100000, data: { chat: { id: '123', flag: 'done' }, usage: { total_tokens: 1 } } })}\n\n`, { headers: { 'Content-Type': 'text/event-stream' } })
  }) as typeof fetch
  const { result } = renderHook(() => useChat(onCompleted, onTitle))
  act(() => { result.current.setDraft('first draft'); result.current.setModelId('first model') })
  await act(async () => { await result.current.send('attempt') })
  const firstKey = result.current.sessionKey
  assert.equal(result.current.turns[0].status, 'error')
  await act(async () => { await result.current.select('') })
  assert.equal(result.current.draft, '')
  act(() => { result.current.setDraft('second draft'); result.current.setModelId('second model') })
  await act(async () => { await result.current.select(firstKey) })
  assert.equal(result.current.draft, 'first draft')
  assert.equal(result.current.modelId, 'first model')
  fail = false
  await act(async () => { await result.current.send('retry') })
  assert.equal(result.current.turns.length, 1)
  assert.equal(result.current.turns[0].turn_index, 1)
  assert.equal(result.current.turns[0].user_content, 'retry')
})
