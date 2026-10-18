/// <reference types="node" />
import { after, afterEach, describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { Window } from 'happy-dom'
import { setConfirmRunningTurnDelay, useChat } from './useChat'
import { useConversations } from './useConversations'
import type { Reference } from './types'

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
  it('revalidates cached project ownership without replacing history or discarding the draft', async () => {
    let detached = false
    let release!: (value: Response) => void
    const historyRequests: string[] = []
    globalThis.fetch = (async url => {
      const path = new URL(String(url), 'http://localhost').pathname
      if (path.endsWith('/turns')) {
        historyRequests.push(path)
        return response({ items: [turn], page: 1, total_pages: 1 })
      }
      if (path.endsWith('/approvals')) return response([])
      const id = path.endsWith('/123') ? '123' : '456'
      if (id === '123' && detached) return new Promise<Response>(resolve => { release = resolve })
      return response({ ...detail, id, is_project: true, project_id: id === '123' ? 'project-a' : 'project-b' })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    act(() => {
      result.current.setDraft('保留草稿 @example.md')
      result.current.setReferences([{ type: 'file', path: 'example.md' }])
    })
    const history = result.current.turns
    await act(async () => { await result.current.select('456') })
    detached = true
    let selected!: Promise<void>
    act(() => { selected = result.current.select('123') })
    assert.equal(result.current.loading, true, 'cached ownership must be checked before enabling project actions')
    assert.equal(result.current.turns, history, 'cached messages remain visible during metadata validation')
    await act(async () => {
      release(response({ ...detail, is_project: false }))
      await selected
    })
    assert.equal(result.current.projectId, undefined)
    assert.equal(result.current.conversation?.project_id, undefined)
    assert.equal(result.current.conversation?.is_project, false)
    assert.deepEqual(result.current.references, [], 'deleted workspace selections must not carry into a normal conversation')
    assert.equal(result.current.draft, '保留草稿 @example.md')
    assert.equal(result.current.turns, history)
    assert.equal(result.current.localSessions.find(item => item.id === '123')?.projectId, undefined)
    assert.equal(result.current.localSessions.find(item => item.id === '456')?.projectId, 'project-b')
    assert.equal(historyRequests.length, 2, 'cached navigation must not refetch message pages')
  })

  it('ignores a late cached ownership response after navigation and retains valid workspace selections', async () => {
    let delayed = false
    let release!: (value: Response) => void
    let signal: AbortSignal | null | undefined
    globalThis.fetch = (async (url, init) => {
      const path = new URL(String(url), 'http://localhost').pathname
      if (path.endsWith('/turns')) return response({ items: [turn], page: 1, total_pages: 1 })
      if (path.endsWith('/approvals')) return response([])
      const id = path.endsWith('/123') ? '123' : '456'
      if (id === '123' && delayed) {
        signal = init?.signal
        return new Promise<Response>(resolve => { release = resolve })
      }
      return response({ ...detail, id, is_project: true, project_id: `project-${id}` })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    act(() => result.current.setReferences([{ type: 'file', path: 'example.md' }]))
    await act(async () => { await result.current.select('456') })
    delayed = true
    let stale!: Promise<void>
    act(() => { stale = result.current.select('123') })
    assert.equal(result.current.loading, true)
    await act(async () => { await result.current.select('456') })
    assert.equal(signal?.aborted, true)
    await act(async () => { release(response({ ...detail, is_project: false })); await stale })
    assert.equal(result.current.id, '456')
    assert.equal(result.current.projectId, 'project-456')
    assert.equal(result.current.loading, false)
    delayed = false
    await act(async () => { await result.current.select('123') })
    assert.equal(result.current.projectId, 'project-123')
    assert.deepEqual(result.current.references, [{ type: 'file', path: 'example.md' }])
  })

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

  it('releases loading ownership when disabled and ignores a late aborted response', async () => {
    let calls = 0
    let release!: (value: Response) => void
    globalThis.fetch = (async () => {
      calls++
      return new Promise<Response>(resolve => { release = resolve })
    }) as typeof fetch
    const { result, rerender } = renderHook(({ enabled }) => useConversations(undefined, { enabled }), { initialProps: { enabled: false } })
    await waitFor(() => assert.equal(result.current.loading, false))
    assert.equal(calls, 0)
    rerender({ enabled: true })
    await waitFor(() => assert.equal(calls, 1))
    rerender({ enabled: false })
    await waitFor(() => assert.equal(result.current.loading, false))
    await act(async () => { release(response({ items: [detail], total: 1 })); await new Promise(resolve => setTimeout(resolve, 0)) })
    assert.deepEqual(result.current.items, [])
  })

  it('keeps the new request busy when an aborted request rejects during debounce or a replacement load', async () => {
    const pending: Array<{ resolve: (value: Response) => void; reject: (err: Error) => void; signal?: AbortSignal | null; page: string | null }> = []
    globalThis.fetch = (async (url, init) => new Promise<Response>((resolve, reject) => pending.push({ resolve, reject, signal: init?.signal, page: new URL(String(url), 'http://localhost').searchParams.get('page') }))) as typeof fetch
    const { result, unmount } = renderHook(() => useConversations())
    await waitFor(() => assert.equal(pending.length, 1))
    act(() => result.current.search('new'))
    assert.equal(pending[0].signal?.aborted, true)
    await act(async () => { pending[0].reject(new Error('late abort')); await Promise.resolve() })
    assert.equal(result.current.loading, true, 'old finally cleared debounce loading')
    await waitFor(() => assert.equal(pending.length, 2))
    let quiet!: Promise<void>
    act(() => { quiet = result.current.refreshQuietly() })
    assert.equal(pending.length, 3)
    await act(async () => { pending[1].resolve(response({ items: [{ ...detail, id: 'stale' }], total: 9 })); await Promise.resolve() })
    assert.equal(result.current.loading, true, 'old finally released replacement loading')
    await act(async () => { pending[2].resolve(response({ items: [detail], total: 2 })); await quiet })
    assert.deepEqual(result.current.items.map(item => item.id), ['123'])
    act(() => { result.current.loadMore(); result.current.loadMore() })
    assert.equal(pending.length, 4)
    assert.equal(pending[3].page, '2')
    unmount()
    assert.equal(pending[3].signal?.aborted, true)
    await act(async () => { pending[3].reject(new Error('late unmount')); await Promise.resolve() })
    await result.current.refreshQuietly()
    assert.equal(pending.length, 4, 'unmounted hook sent a new request')
  })

  it('does not send quiet/filter requests while disabled and resumes pagination after enabling', async () => {
    const pages: string[] = []
    globalThis.fetch = (async url => {
      const page = new URL(String(url), 'http://localhost').searchParams.get('page')!
      pages.push(page)
      return response({ items: [{ ...detail, id: page }], total: 2 })
    }) as typeof fetch
    const { result, rerender } = renderHook(({ enabled }) => useConversations(undefined, { enabled }), { initialProps: { enabled: false } })
    act(() => { result.current.search('saved'); result.current.filter(true); result.current.resetFilters() })
    await act(async () => { await result.current.refreshQuietly() })
    assert.equal(result.current.loading, false)
    assert.deepEqual(pages, [])
    rerender({ enabled: true })
    await waitFor(() => assert.equal(result.current.items.length, 1))
    act(() => result.current.loadMore())
    await waitFor(() => assert.equal(result.current.items.length, 2))
    assert.deepEqual(pages, ['1', '2'])
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

  it('refreshes detail/list on start without waiting for title and applies independent events', async () => {
    let titleRequests = 0
    globalThis.fetch = (async url => {
      const path = String(url)
      if (path.endsWith('/sse')) {
        return new Response(new ReadableStream({ start(controller) {
          controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ code: 100000, data: { chat: { id: 'new-conv', flag: 'start' } } })}\n\n`))
          controller.close()
        } }), { headers: { 'Content-Type': 'text/event-stream' } })
      }
      if (path.endsWith('/title/wait')) {
        titleRequests++
        return response({ id: 'new-conv', title: 'unexpected' })
      }
      return response({ id: 'new-conv', title: '新对话', turn_count: 0 })
    }) as typeof fetch
    let refreshes = 0
    const titles: string[] = []
    const { result } = renderHook(() => useChat(async () => { refreshes++ }, title => titles.push(title.title)))
    await act(async () => { await result.current.send('新会话') })
    assert.equal(result.current.error, '')
    await waitFor(() => assert.equal(refreshes, 1))
    await waitFor(() => assert.equal(result.current.conversation?.title, '新对话'))
    assert.equal(titleRequests, 0)
    act(() => { result.current.applyTitleEvent({ id: 'new-conv', title: '自动标题', revision: 1 }) })
    assert.deepEqual(titles, [])
    assert.equal(result.current.conversation?.title, '自动标题')
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

describe('title reconciliation in list and sessions', () => {
  it('manual detail reload clears only a current consistent title diagnostic', async () => {
    let revision = 1
    globalThis.fetch = (async url => {
      const path = new URL(String(url), 'http://localhost').pathname
      if (path.endsWith('/turns')) return response({ items: [turn], page: 1, total_pages: 1 })
      if (path.endsWith('/approvals')) return response([])
      return response({ ...detail, title_revision: revision, title_job_status: 'canceled', title_job_revision: revision })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    act(() => { result.current.applyTitleEvent({ id: '123', title: 'newer', revision: 3, job_status: 'failed', job_revision: 3 }); result.current.titleSyncError('safe diagnostic') })
    await act(async () => { await result.current.select('123') })
    assert.equal(result.current.titleError, 'safe diagnostic')
    revision = 4
    await act(async () => { await result.current.select('123') })
    assert.equal(result.current.titleError, '')
    assert.equal(result.current.conversation?.title_revision, 4)
  })
  it('clears old title/job overlays when refreshed DB revisions advance', async () => {
    let stored = { ...detail, title: 'db-1', title_revision: 1, title_job_status: 'pending', title_job_revision: 0 }
    globalThis.fetch = (async () => response({ items: [stored], total: 1 })) as typeof fetch
    const { result } = renderHook(() => useConversations())
    await waitFor(() => assert.equal(result.current.items.length, 1))
    act(() => { result.current.updateTitle({ id: '123', title: 'event-2', revision: 2, job_status: 'failed', job_revision: 3 }) })
    assert.equal(result.current.items[0].title, 'event-2')
    stored = { ...stored, title: 'db-4', title_revision: 4, title_job_status: 'canceled', title_job_revision: 5 }
    await act(async () => { await result.current.refreshQuietly() })
    assert.equal(result.current.items[0].title, 'db-4'); assert.equal(result.current.items[0].title_job_status, 'canceled')
    act(() => { result.current.updateTitle({ id: '123', title: 'late-2', revision: 2, job_status: 'running', job_revision: 2 }) })
    assert.equal(result.current.items[0].title, 'db-4'); assert.equal(result.current.items[0].title_job_revision, 5)
    stored = { ...stored, title: 'db-6', title_revision: 6 }
    await act(async () => { await result.current.refreshQuietly() })
    assert.equal(result.current.items[0].title, 'db-6')
  })
  it('retains a confirmed overlay on conflicting same-version page data without regressing later job events', async () => {
    let stored = { ...detail, title: 'initial', title_revision: 0 }
    globalThis.fetch = (async () => response({ items: [stored], total: 1 })) as typeof fetch
    const { result } = renderHook(() => useConversations())
    await waitFor(() => assert.equal(result.current.items.length, 1))
    act(() => { result.current.updateTitle({ id: '123', title: 'confirmed', revision: 1 }) })
    stored = { ...stored, title: 'conflicting', title_revision: 1 }
    await act(async () => { await result.current.refreshQuietly() })
    assert.equal(result.current.items[0].title, 'confirmed')
    act(() => { result.current.updateTitle({ id: '123', job_status: 'failed', job_revision: 4 }) })
    assert.equal(result.current.items[0].title, 'confirmed'); assert.equal(result.current.items[0].title_job_status, 'failed')
  })
  it('detail/rename merges title and job independently and targets the matching session', async () => {
    globalThis.fetch = (async url => {
      const path = new URL(String(url), 'http://localhost').pathname
      if (path.endsWith('/turns')) return response({ items: [turn], page: 1, total_pages: 1 })
      if (path.endsWith('/approvals')) return response([])
      return response({ ...detail, id: path.endsWith('/456') ? '456' : '123', title_revision: 1, title_job_revision: 0 })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    act(() => { result.current.applyTitleEvent({ id: '123', title: 'manual', revision: 5, job_status: 'running', job_revision: 3 }) })
    await act(async () => { await result.current.select('456') })
    const original = result.current.conversation!
    act(() => { result.current.setConversation({ ...original, id: '123', title: 'stale', title_revision: 2, title_job_status: 'failed', title_job_revision: 4 }) })
    assert.equal(result.current.conversation?.id, '456'); assert.equal(result.current.conversation?.title, '已有标题')
    await act(async () => { await result.current.select('123') })
    assert.equal(result.current.conversation?.title, 'manual'); assert.equal(result.current.conversation?.title_job_status, 'failed')
  })
  it('a late explicit title after navigation or removal cannot update another session', async () => {
    let release!: (value: Response) => void
    globalThis.fetch = (async url => {
      const path = new URL(String(url), 'http://localhost').pathname
      if (path.endsWith('/title/wait')) return new Promise<Response>(resolve => { release = resolve })
      if (path.endsWith('/turns')) return response({ items: [turn], page: 1, total_pages: 1 })
      if (path.endsWith('/approvals')) return response([])
      return response({ ...detail, id: path.endsWith('/456') ? '456' : '123', title: '新对话', title_revision: 0 })
    }) as typeof fetch
    const titles: string[] = []
    const { result } = renderHook(() => useChat(onCompleted, value => titles.push(value.title)))
    await act(async () => { await result.current.select('123') })
    let task!: Promise<void>
    act(() => { task = result.current.generateTitle() })
    await act(async () => { await result.current.select('456') })
    await act(async () => { release(response({ id: '123', title: 'stale', revision: 1 })); await task })
    assert.equal(result.current.conversation?.id, '456'); assert.equal(result.current.conversation?.title, '新对话'); assert.deepEqual(titles, [])
    await act(async () => { await result.current.select('123') })
    act(() => { task = result.current.generateTitle(); result.current.removeConversation('123') })
    await act(async () => { release(response({ id: '123', title: 'late', revision: 2 })); await task })
    assert.equal(result.current.id, ''); assert.deepEqual(result.current.knownConversationIDs, ['456']); assert.deepEqual(titles, [])
  })
})

describe('independent conversation streams', () => {
  it('keeps interleaved streams isolated before IDs arrive and cancels only the selected one', async () => {
    const streams: Array<{ emit: (id: string, flag: string, text?: string) => void; signal: AbortSignal }> = []
    const requested: string[] = []
    globalThis.fetch = (async (url, init) => {
      requested.push(String(url))
      if (!String(url).endsWith('/sse')) {
        const id = String(url).split('/').at(-1)
        // 首条流是项目会话；详情必须与已持久化的请求归属一致。
        return response({ ...detail, id, is_project: id === '111', project_id: id === '111' ? 'project-a' : undefined })
      }
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
    assert.ok(requested.some(item => item.endsWith('/111/stop')))
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

  it('keeps interrupted turns when continuing and places the next turn after them', async () => {
    const interrupted = { turn_index: 2, user_content: '中断', blocks: [{ type: 'text', text: 'partial' }], started_at: new Date().toISOString(), status: 'interrupted' }
    globalThis.fetch = (async url => {
      const path = new URL(String(url), 'http://localhost')
      if (path.pathname.endsWith('/turns')) return response({ items: [{ ...turn, turn_index: 1 }, interrupted], page: 1, total: 2, total_pages: 1, page_size: 5 })
      if (path.pathname.endsWith('/sse')) {
        return new Response(`data: ${JSON.stringify({ code: 100000, data: { chat: { id: '123', flag: 'done' }, usage: { total_tokens: 1 } } })}\n\n`, { headers: { 'Content-Type': 'text/event-stream' } })
      }
      return response({ ...detail, turn_count: 2 })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    assert.deepEqual(result.current.turns.map(item => item.turn_index), [1, 2])
    await act(async () => { await result.current.send('下一轮') })
    assert.equal(result.current.turns.length, 3)
    assert.equal(result.current.turns[1].status, 'interrupted')
    assert.equal(result.current.turns[2].turn_index, 3)
    assert.equal(result.current.turns[2].status, 'done')
  })

  it('refreshes the last page before sending so persisted canceled turns keep the index aligned', async () => {
    const canceled = { turn_index: 2, user_content: '取消的提问', blocks: [{ type: 'text', text: 'partial' }], started_at: new Date().toISOString(), status: 'canceled' }
    let turnPages = 0
    globalThis.fetch = (async url => {
      const path = new URL(String(url), 'http://localhost')
      if (path.pathname.endsWith('/turns')) {
        turnPages++
        return response(turnPages === 1
          ? { items: [{ ...turn, turn_index: 1 }], page: 1, total: 1, total_pages: 1, page_size: 5 }
          : { items: [{ ...turn, turn_index: 1 }, canceled], page: 1, total: 2, total_pages: 1, page_size: 5 })
      }
      if (path.pathname.endsWith('/sse')) {
        return new Response(`data: ${JSON.stringify({ code: 100000, data: { chat: { id: '123', flag: 'done' }, usage: { total_tokens: 1 } } })}\n\n`, { headers: { 'Content-Type': 'text/event-stream' } })
      }
      return response({ ...detail, turn_count: 1 })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    assert.deepEqual(result.current.turns.map(item => item.turn_index), [1])
    await act(async () => { await result.current.send('下一轮') })
    assert.deepEqual(result.current.turns.map(item => item.turn_index), [1, 2, 3])
    assert.equal(result.current.turns[1].status, 'canceled')
    assert.equal(result.current.turns[2].status, 'done')
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

describe('stop and delayed confirmation races', () => {
  // 停止通知延迟到达时，只能中止点击瞬间的那一轮，不能取消用户随后开始的新轮次。
  it('stops only the turn captured at click time', async () => {
    // 停止请求挂在网络上：800ms 兜底先 abort 第一轮，随后第二轮开始，最后停止请求才返回；
    // 实现若在 finally 读取可变的 session.stream 就会误杀第二轮。
    const encoder = new TextEncoder()
    const frame = (flag: string) => encoder.encode(`data: ${JSON.stringify({ code: 100000, data: { chat: { id: '123', flag }, usage: { total_tokens: 0 } } })}\n\n`)
    const streams: Array<{ signal: AbortSignal; finish: () => void }> = []
    let releaseStop!: () => void
    const stopResponse = new Promise<Response>(resolve => { releaseStop = () => resolve(response(null)) })
    globalThis.fetch = (async (url, init) => {
      const parsed = new URL(String(url), 'http://localhost')
      if (parsed.pathname.endsWith('/sse')) {
        const signal = init!.signal as AbortSignal
        return new Response(new ReadableStream({ start(controller) {
          let finished = false
          streams.push({ signal, finish() {
            if (finished) return
            finished = true
            try { controller.enqueue(frame('done')) } catch { /* aborted */ }
            queueMicrotask(() => { try { controller.close() } catch { /* closed */ } })
          } })
          controller.enqueue(frame('start'))
          signal.addEventListener('abort', () => controller.error(new DOMException('Aborted', 'AbortError')))
        } }), { headers: { 'Content-Type': 'text/event-stream' } })
      }
      if (parsed.pathname.endsWith('/stop')) return stopResponse
      if (parsed.pathname.endsWith('/turns')) return response({ items: [], page: 1, total: 0, total_pages: 1, page_size: 5 })
      return response({ ...detail, id: '123', title: '已有标题', turn_count: 1 })
    }) as typeof fetch

    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    let first!: Promise<void>
    act(() => { first = result.current.send('第一轮') })
    await waitFor(() => assert.equal(result.current.id, '123'))
    const firstStream = streams[0]
    assert.ok(firstStream)

    act(() => { result.current.stop() })
    // 停止请求未返回：800ms 兜底 abort 第一轮。
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 900)) })
    assert.equal(firstStream.signal.aborted, true)

    // 第一轮结束（断联路径）后开始第二轮。
    firstStream.finish()
    await act(async () => { await first })
    let second!: Promise<void>
    act(() => { second = result.current.send('第二轮') })
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
    const secondStream = streams[1]
    assert.ok(secondStream)
    assert.notEqual(firstStream.signal, secondStream.signal)

    // 旧停止响应到达：不能 abort 第二轮。
    await act(async () => { releaseStop(); await new Promise(resolve => setTimeout(resolve, 20)) })
    assert.equal(secondStream.signal.aborted, false)

    // 收尾。
    secondStream.finish()
    await act(async () => { await second })
  })

  // 等待确认期间发生的新操作必须使旧确认失效，不能整体覆盖新的轮次列表。
  it('ignores a delayed reconciliation after a new turn starts', async () => {
    setConfirmRunningTurnDelay(10)
    after(() => { setConfirmRunningTurnDelay(3000) })
    const encoder = new TextEncoder()
    const frame = (flag: string) => encoder.encode(`data: ${JSON.stringify({ code: 100000, data: { chat: { id: '123', flag }, usage: { total_tokens: 0 } } })}\n\n`)
    const running = { ...turn, status: 'running' }
    const stale = { ...turn, status: 'interrupted', user_content: '旧确认结果' }

    let delayConfirm = false
    let confirmRequests = 0
    let releaseConfirm!: (value: Response) => void
    globalThis.fetch = (async (url, init) => {
      const parsed = new URL(String(url), 'http://localhost')
      if (parsed.pathname.endsWith('/sse')) {
        const signal = init!.signal as AbortSignal
        return new Response(new ReadableStream({ start(controller) {
          controller.enqueue(frame('done'))
          queueMicrotask(() => controller.close())
          signal.addEventListener('abort', () => controller.error(new DOMException('Aborted', 'AbortError')))
        } }), { headers: { 'Content-Type': 'text/event-stream' } })
      }
      if (parsed.pathname.endsWith('/turns')) {
        if (delayConfirm) {
          delayConfirm = false
          confirmRequests++
          return new Promise<Response>(resolve => { releaseConfirm = resolve })
        }
        return response({ items: [{ ...running }], page: 1, total: 1, total_pages: 1, page_size: 5 })
      }
      return response({ ...detail, turn_count: 1 })
    }) as typeof fetch

    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    // 首轮完成后才开始延迟确认的请求。
    delayConfirm = true
    await waitFor(() => assert.equal(confirmRequests, 1))

    // 确认在途时用户发送新一轮：发送会提升操作版本。
    await act(async () => { await result.current.send('新一轮') })
    assert.ok(result.current.turns.some(item => item.user_content === '新一轮'))

    // 迟到的确认响应必须被丢弃，不能整体覆盖发送后的列表。
    await act(async () => { releaseConfirm(response({ items: [stale], page: 1, total: 1, total_pages: 1, page_size: 5 })); await new Promise(resolve => setTimeout(resolve, 20)) })
    assert.ok(result.current.turns.some(item => item.user_content === '新一轮'))
    assert.ok(!result.current.turns.some(item => item.user_content === '旧确认结果'))
  })
})

describe('conversation list refresh cost', () => {
  // 刷新不能按已加载页数放大请求：30 页后的刷新必须限制总请求数与并发。
  it('bounds request count and concurrency when refreshing many loaded pages', async () => {
    const requested: number[] = []
    let inFlight = 0
    let peak = 0
    globalThis.fetch = (async url => {
      const page = Number(new URL(String(url), 'http://localhost').searchParams.get('page'))
      requested.push(page)
      inFlight++
      peak = Math.max(peak, inFlight)
      await new Promise(resolve => setTimeout(resolve, 5))
      inFlight--
      const item = { ...detail, id: `conv-${page}`, turn_count: 1 }
      return response({ items: [item], total: 600, page, page_size: 20 })
    }) as typeof fetch

    const { result } = renderHook(() => useConversations())
    await waitFor(() => assert.equal(result.current.items.length, 1))
    // 模拟用户连续加载到第 30 页；等待每一页真正落地。
    for (let page = 2; page <= 30; page++) {
      await act(async () => { result.current.loadMore() })
      await waitFor(() => assert.equal(result.current.items.length, page))
    }

    const before = requested.length
    await act(async () => { result.current.refresh() })
    await waitFor(() => assert.ok(requested.length > before))
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 50)) })
    const refreshRequests = requested.slice(before)
    assert.ok(refreshRequests.length <= 4, `refresh issued ${refreshRequests.length} requests`)
    assert.ok(peak <= 4, `peak concurrency ${peak}`)
    // 第一页与末尾页都在刷新范围内，保证最新列表与用户位置都更新。
    assert.ok(refreshRequests.includes(1))
    assert.ok(refreshRequests.includes(30))
    assert.equal(result.current.items[0].id, 'conv-1')
    assert.equal(result.current.items.at(-1)?.id, 'conv-30')
  })

  // 刷新只重取首尾页时，中间已加载页不能被刷新结果覆盖丢失。
  it('keeps loaded middle pages after a quiet refresh', async () => {
    globalThis.fetch = (async url => {
      const page = Number(new URL(String(url), 'http://localhost').searchParams.get('page'))
      return response({ items: [{ ...detail, id: `conv-${page}` }], total: 5, page, page_size: 1 })
    }) as typeof fetch

    const { result } = renderHook(() => useConversations())
    await waitFor(() => assert.equal(result.current.items.length, 1))
    for (let page = 2; page <= 5; page++) {
      await act(async () => { result.current.loadMore() })
      await waitFor(() => assert.equal(result.current.items.length, page))
    }
    await act(async () => { await result.current.refreshQuietly() })
    assert.deepEqual(result.current.items.map(item => item.id), ['conv-1', 'conv-2', 'conv-3', 'conv-4', 'conv-5'])
    assert.equal(result.current.total, 5)
  })
})

describe('conversation defaults on open', () => {
  it('defaults an opened conversation to its last used model until the user chooses another', async () => {
    const sent: Array<string | undefined> = []
    globalThis.fetch = (async (url, init) => {
      const path = new URL(String(url), 'http://localhost')
      if (path.pathname.endsWith('/turns')) return response({ items: [turn], page: 1, total: 1, total_pages: 1, page_size: 5 })
      if (path.pathname.endsWith('/sse')) {
        sent.push(JSON.parse(String(init?.body)).model_id)
        return new Response(`data: ${JSON.stringify({ code: 100000, data: { chat: { id: '123', flag: 'done' }, usage: { total_tokens: 1 } } })}\n\n`, { headers: { 'Content-Type': 'text/event-stream' } })
      }
      return response({ ...detail, last_model_id: 'local-model' })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    assert.equal(result.current.modelId, 'local-model')
    await act(async () => { await result.current.send('第一轮', result.current.modelId) })
    assert.equal(sent[0], 'local-model')
    // 手动选择后重新加载历史不再回落到会话最近模型。
    act(() => { result.current.setModelId('manual-model') })
    await act(async () => { await result.current.select('123') })
    assert.equal(result.current.modelId, 'manual-model')
    await act(async () => { await result.current.send('第二轮', result.current.modelId) })
    assert.equal(sent[1], 'manual-model')
  })

  it('only generates a default title on explicit action, never when opening detail', async () => {
    let titleRequests = 0
    globalThis.fetch = (async url => {
      const path = new URL(String(url), 'http://localhost')
      if (path.pathname.endsWith('/title/wait')) {
        titleRequests++
        return response({ id: '123', title: '自动标题' })
      }
      if (path.pathname.endsWith('/turns')) return response({ items: [], page: 1, total: 0, total_pages: 1, page_size: 5 })
      const id = path.pathname.endsWith('/456') ? '456' : '123'
      return response({ ...detail, id, title: id === '456' ? '已有标题' : '新对话' })
    }) as typeof fetch
    const titles: string[] = []
    const { result } = renderHook(() => useChat(onCompleted, title => titles.push(title.title)))
    await act(async () => { await result.current.select('123') })
    assert.equal(result.current.conversation?.title, '新对话')
    assert.equal(titleRequests, 0)
    await act(async () => { await result.current.generateTitle() })
    await waitFor(() => assert.equal(result.current.conversation?.title, '自动标题'))
    assert.equal(titleRequests, 1)
    assert.deepEqual(titles, ['自动标题'])
    // 已有标题的会话不重复请求生成。
    await act(async () => { await result.current.select('456') })
    await act(async () => { await result.current.generateTitle() })
    assert.equal(result.current.conversation?.title, '已有标题')
    assert.equal(titleRequests, 1)
  })

  it('explicit unavailable generation reports failure without changing title or session error', async () => {
    globalThis.fetch = (async url => {
      const path = new URL(String(url), 'http://localhost')
      if (path.pathname.endsWith('/title/wait')) return Response.json({ code: 102007, message: '任务模型未配置' })
      if (path.pathname.endsWith('/turns')) return response({ items: [], page: 1, total: 0, total_pages: 1, page_size: 5 })
      return response({ ...detail, title: '新对话' })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    await act(async () => { await assert.rejects(result.current.generateTitle(), /任务模型未配置/) })
    assert.equal(result.current.error, '')
    assert.equal(result.current.conversation?.title, '新对话')
  })

  it('cached navigation does not retry titles; explicit retry remains available', async () => {
    let titleRequests = 0
    globalThis.fetch = (async url => {
      const path = new URL(String(url), 'http://localhost')
      if (path.pathname.endsWith('/title/wait')) {
        titleRequests++
        return response({ id: '123', title: titleRequests === 1 ? '新对话' : '自动标题' })
      }
      if (path.pathname.endsWith('/turns')) return response({ items: [turn], page: 1, total: 1, total_pages: 1, page_size: 5 })
      const id = path.pathname.endsWith('/456') ? '456' : '123'
      return response({ ...detail, id, title: id === '456' ? '已有标题' : '新对话' })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    // 首次明确补生成未取得标题：保留默认文本。
    await act(async () => { await result.current.select('123') })
    assert.equal(titleRequests, 0)
    await act(async () => { await result.current.generateTitle() })
    await waitFor(() => assert.equal(titleRequests, 1))
    assert.equal(result.current.conversation?.title, '新对话')
    await act(async () => { await result.current.select('456') })
    assert.equal(result.current.conversation?.title, '已有标题')
    assert.equal(titleRequests, 1)
    // 会话仍在缓存中：重新打开只展示，明确重试才生成。
    await act(async () => { await result.current.select('123') })
    assert.equal(titleRequests, 1)
    await act(async () => { await result.current.generateTitle() })
    await waitFor(() => assert.equal(titleRequests, 2))
    await waitFor(() => assert.equal(result.current.conversation?.title, '自动标题'))
    // 标题已生成，再切换到其它会话又切回来不再请求。
    await act(async () => { await result.current.select('456') })
    await act(async () => { await result.current.select('123') })
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
    assert.equal(titleRequests, 2)
  })
})

it('reconciles failed preparation audit from the latest history page without losing the view', async () => {
  setConfirmRunningTurnDelay(10)
  try {
    const audit = { version: 1, summary_calls: 2, reported_calls: 1, unreported_calls: 1,
      usage: { input_tokens: 10, output_tokens: 20, total_tokens: 42, cache_hit_tokens: 5, cache_creation_tokens: 7, reasoning_tokens: 3 } }
    const saved = { ...turn, turn_index: 10, status: 'completed' }
    const failed = { ...turn, turn_index: 11, user_content: 'inspect missing', status: 'failed', preparation_usage: audit }
    let ended = false
    const pages: number[] = []
    globalThis.fetch = (async url => {
      const parsed = new URL(String(url), 'http://localhost')
      if (parsed.pathname.endsWith('/sse')) {
        ended = true
        const start = { code: 100000, data: { chat: { id: '123', flag: 'start' }, usage: { total_tokens: 0 } } }
        const error = { code: 102017, message: 'reference unavailable', data: { chat: { id: '123', flag: 'error' } } }
        return new Response(`data: ${JSON.stringify(start)}\n\ndata: ${JSON.stringify(error)}\n\n`, { headers: { 'Content-Type': 'text/event-stream' } })
      }
      if (parsed.pathname.endsWith('/turns')) {
        const page = Number(parsed.searchParams.get('page'))
        pages.push(page)
        return response({ items: page === 1 ? [] : ended ? [saved, failed] : [saved], page: 3, total: ended ? 11 : 10, total_pages: 3, page_size: 5 })
      }
      return response({ ...detail, title: '已有标题', turn_count: 10 })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    await act(async () => { await result.current.send('inspect missing') })
    const key = result.current.viewKey
    act(() => result.current.setDraft('keep unsent draft'))
    await waitFor(() => assert.equal(result.current.turns.at(-1)?.preparation_usage?.unreported_calls, 1))
    assert.equal(pages.at(-1), 1000000)
    assert.equal(result.current.viewKey, key)
    assert.equal(result.current.draft, 'keep unsent draft')
    assert.equal(result.current.turns.at(-1)?.status, 'failed')
  } finally { setConfirmRunningTurnDelay(3000) }
})

describe('reference intent recovery', () => {
  const requested = { preparation_state: 'requested', sources: [{ type: 'directory', path: 'pkg' }] }
  const failed = { ...turn, id: 'turn-1', status: 'failed', reference_summary: requested }
  const refs: Reference[] = [{ type: 'directory', path: 'pkg' }]
  const page = () => response({ items: [failed], page: 1, total_pages: 1 })
  const failure = () => new Response(`data: ${JSON.stringify({ code: 102017, message: 'reference unavailable', data: { chat: { id: '123', flag: 'error' }, reference_summary: requested } })}\n\n`)

  it('restores history choices only after confirming project ownership without sending', async () => {
    let calls = 0
    globalThis.fetch = (async url => {
      calls++
      const path = String(url)
      if (path.endsWith('/approvals')) return response([])
      if (path.includes('/turns')) return page()
      assert.ok(!path.endsWith('/sse'))
      return response({ ...detail, project_id: 'project-1' })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    act(() => result.current.setDraft('keep my question'))
    const before = calls
    await act(async () => { assert.equal(await result.current.restoreReferences(result.current.turns[0]), 1) })
    assert.equal(calls, before + 1)
    assert.deepEqual(result.current.references, refs)
    assert.equal(result.current.draft, 'keep my question')
  })

  it('rejects restoring choices after a project has been unlinked', async () => {
    let unlinked = false
    globalThis.fetch = (async url => String(url).includes('/turns') ? page() : String(url).endsWith('/approvals') ? response([]) : response({ ...detail, project_id: unlinked ? undefined : 'project-1' })) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    unlinked = true
    await act(async () => { assert.equal(await result.current.restoreReferences(result.current.turns[0]), 0) })
    assert.deepEqual(result.current.references, [])
    assert.equal(result.current.projectId, undefined)
    assert.match(result.current.error, /解除项目归属/)
  })

  it('does not replace newly edited choices with a late ownership response', async () => {
    let release: ((value: Response) => void) | undefined
    let deferred = false
    globalThis.fetch = (async url => {
      if (String(url).includes('/turns')) return page()
      if (String(url).endsWith('/approvals')) return response([])
      if (deferred) return new Promise<Response>(resolve => { release = resolve })
      return response({ ...detail, project_id: 'project-1' })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    deferred = true
    let restoring!: Promise<number>
    act(() => { restoring = result.current.restoreReferences(result.current.turns[0]) })
    await waitFor(() => assert.ok(release))
    const newer: Reference[] = [{ type: 'file', path: 'new.go' }]
    act(() => { result.current.setDraft('new question'); result.current.setReferences(newer) })
    await act(async () => { release!(response({ ...detail, project_id: 'project-1' })); assert.equal(await restoring, 0) })
    assert.deepEqual(result.current.references, newer)
    assert.equal(result.current.draft, 'new question')
    assert.equal(result.current.loading, false)
  })

  it('does not apply a late history recovery after navigating to another session', async () => {
    let release!: (value: Response) => void
    let deferred = false
    globalThis.fetch = (async url => {
      const path = String(url)
      if (path.includes('/turns')) return page()
      if (path.endsWith('/approvals')) return response([])
      if (deferred && path.endsWith('/123')) return new Promise<Response>(resolve => { release = resolve })
      return response({ ...detail, id: path.endsWith('/456') ? '456' : '123', project_id: 'project-1' })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    deferred = true
    let restoring!: Promise<number>
    act(() => { restoring = result.current.restoreReferences(result.current.turns[0]) })
    await waitFor(() => assert.ok(release))
    await act(async () => { await result.current.select('456') })
    await act(async () => { release(response({ ...detail, project_id: 'project-1' })); assert.equal(await restoring, 0) })
    assert.equal(result.current.id, '456')
    assert.deepEqual(result.current.references, [])
    assert.equal(result.current.loading, false)
  })

  it('keeps a validated error audit and auto-restores unchanged choices', async () => {
    globalThis.fetch = (async url => String(url).endsWith('/sse') ? failure() : String(url).includes('/turns') ? page() : String(url).endsWith('/approvals') ? response([]) : response({ ...detail, project_id: 'project-1' })) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    act(() => result.current.setReferences(refs))
    await act(async () => { await result.current.send('inspect') })
    assert.deepEqual(result.current.references, refs)
    assert.equal(result.current.turns.at(-1)?.reference_summary?.preparation_state, 'requested')
    assert.equal(result.current.turns.at(-1)?.status, 'error')
    assert.equal(result.current.streaming, false)
  })

  it('does not auto-restore after edits during a failed generation', async () => {
    let release!: (value: Response) => void
    globalThis.fetch = (async url => String(url).endsWith('/sse') ? new Promise<Response>(resolve => { release = resolve }) : String(url).includes('/turns') ? page() : String(url).endsWith('/approvals') ? response([]) : response({ ...detail, project_id: 'project-1' })) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    act(() => result.current.setReferences(refs))
    let sending!: Promise<void>
    act(() => { sending = result.current.send('inspect') })
    await waitFor(() => assert.ok(release))
    const newer: Reference[] = [{ type: 'file', path: 'new.go' }]
    act(() => { result.current.setDraft('new question'); result.current.setReferences(newer) })
    await act(async () => { release(failure()); await sending })
    assert.deepEqual(result.current.references, newer)
    assert.equal(result.current.draft, 'new question')
    assert.equal(result.current.turns.at(-1)?.reference_summary?.sources[0].path, 'pkg')
  })

  it('rejects a recovery response with another conversation identity', async () => {
    let wrong = false
    globalThis.fetch = (async url => String(url).includes('/turns') ? page() : String(url).endsWith('/approvals') ? response([]) : response({ ...detail, id: wrong ? '456' : '123', project_id: 'project-1' })) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    wrong = true
    await act(async () => { assert.equal(await result.current.restoreReferences(result.current.turns[0]), 0) })
    assert.deepEqual(result.current.references, [])
    assert.equal(result.current.id, '123')
    assert.match(result.current.error, /身份不匹配/)
  })

  it('does not auto-restore when the authoritative detail has no project', async () => {
    let ended = false
    globalThis.fetch = (async url => {
      if (String(url).endsWith('/sse')) { ended = true; return failure() }
      if (String(url).includes('/turns')) return page()
      if (String(url).endsWith('/approvals')) return response([])
      return response({ ...detail, project_id: ended ? undefined : 'project-1' })
    }) as typeof fetch
    const { result } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    act(() => result.current.setReferences(refs))
    await act(async () => { await result.current.send('inspect') })
    assert.deepEqual(result.current.references, [])
    assert.equal(result.current.projectId, undefined)
    assert.equal(result.current.turns.at(-1)?.reference_summary?.preparation_state, 'requested')
  })

  it('does not start an ownership request after unmount cancels a stream', async () => {
    let requests = 0
    let started = false
    globalThis.fetch = (async (url, init) => {
      if (String(url).endsWith('/sse')) {
        started = true
        return new Response(new ReadableStream({ start(controller) { init!.signal!.addEventListener('abort', () => controller.error(new DOMException('Aborted', 'AbortError'))) } }))
      }
      requests++
      if (String(url).includes('/turns')) return page()
      if (String(url).endsWith('/approvals')) return response([])
      return response({ ...detail, project_id: 'project-1' })
    }) as typeof fetch
    const { result, unmount } = renderHook(() => useChat(onCompleted, onTitle))
    await act(async () => { await result.current.select('123') })
    act(() => result.current.setReferences(refs))
    let sending!: Promise<void>
    act(() => { sending = result.current.send('inspect') })
    await waitFor(() => assert.ok(started))
    const before = requests
    unmount()
    await sending
    assert.equal(requests, before)
  })
})
