/// <reference types="node" />
import { after, afterEach, describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { Window } from 'happy-dom'
import type { ReactNode } from 'react'
import { ChatProvider } from './ChatProvider'
import { useWorkspaceChat } from './chatContext'
import { useConversations } from './useConversations'
import { useTitleEvents } from './useTitleEvents'
import type { TitleEvent } from './types'
import { readdirSync } from 'node:fs'

const dom = new Window({ url: 'http://localhost' })
const globals = { window: dom, document: dom.document, navigator: dom.navigator, HTMLElement: dom.HTMLElement, MutationObserver: dom.MutationObserver, IS_REACT_ACT_ENVIRONMENT: true }
const previous = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
const { renderHook, act, cleanup, waitFor } = await import('@testing-library/react')
const originalFetch = globalThis.fetch
afterEach(async () => { cleanup(); await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) }); globalThis.fetch = originalFetch })
after(() => {
  for (const [key, descriptor] of previous) { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else Reflect.deleteProperty(globalThis, key) }
  void dom.happyDOM.close()
})
const response = (data: unknown) => Response.json({ code: 100000, data })
const tick = () => new Promise(resolve => setTimeout(resolve, 0))
const wrapper = ({ children }: { children: ReactNode }) => <ChatProvider>{children}</ChatProvider>
const completed = async () => {}
const titled = () => {}
const base = (id: string) => ({ id, title: '新对话', title_revision: 0, title_job_revision: 0, turn_count: 1 })
function eventStream(signal: AbortSignal) {
  let controller!: ReadableStreamDefaultController<Uint8Array>, canceled = 0
  const body = new ReadableStream<Uint8Array>({ start(value) { controller = value }, cancel() { canceled++ } })
  return { body, signal, controller, canceled: () => canceled,
    emit: (event: TitleEvent) => controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ code: 100000, data: event })}\n\n`)) }
}

describe('title Provider integration', () => {
  it('applies job-only/project/session events before old snapshots without regressions', async () => {
    const streams: ReturnType<typeof eventStream>[] = []
    const snapshots: Array<{ ids: string[]; resolve: (value: Response) => void }> = []
    let titleWaits = 0
    globalThis.fetch = (async (url, init) => {
      const path = new URL(String(url), 'http://localhost')
      if (path.pathname.endsWith('/events')) { const stream = eventStream(init!.signal!); streams.push(stream); return new Response(stream.body, { headers: { 'Content-Type': 'text/event-stream' } }) }
      if (path.pathname.endsWith('/title-snapshots')) {
        const { ids } = JSON.parse(String(init?.body)) as { ids: string[] }
        return new Promise<Response>(resolve => { snapshots.push({ ids, resolve }) })
      }
      if (path.pathname.endsWith('/page')) { const id = path.searchParams.get('project_id') ? 'project' : 'normal'; return response({ items: [base(id)], total: 1 }) }
      if (path.pathname.endsWith('/turns')) return response({ items: [], page: 1, total_pages: 1 })
      if (path.pathname.endsWith('/approvals')) return response([])
      if (path.pathname.endsWith('/title/wait')) { titleWaits++; return response({ id: 'project', title: 'unexpected', revision: 1 }) }
      return response({ ...base('project'), project_id: 'p' })
    }) as typeof fetch
    const { result } = renderHook(() => ({ normal: useConversations(), project: useConversations('p'), chat: useWorkspaceChat(completed, titled) }), { wrapper })
    await waitFor(() => assert.equal(result.current.project.items.length, 1))
    await act(async () => { await result.current.chat.select('project') })
    await waitFor(() => assert.ok(snapshots.length))
    await act(async () => {
      streams[0].emit({ type: 'conversation.title.updated', conversation_id: 'project', title: '手动标题', revision: 2 })
      streams[0].emit({ type: 'conversation.title.job.updated', conversation_id: 'project', job_status: 'canceled', job_revision: 5 })
      streams[0].emit({ type: 'conversation.title.updated', conversation_id: 'normal', title: '普通标题', revision: 1 })
      await tick()
    })
    assert.equal(result.current.project.items[0].title, '手动标题')
    assert.equal(result.current.chat.conversation?.title_job_status, 'canceled')
    await act(async () => {
      for (const request of snapshots) request.resolve(response({ items: request.ids.map(id => ({ id, title: '新对话', title_revision: 0, job_status: null, job_revision: 0 })), missing_ids: [] }))
      await tick()
    })
    assert.equal(result.current.chat.conversation?.title, '手动标题')
    assert.equal(result.current.project.items[0].title_job_revision, 5)
    assert.equal(result.current.normal.items[0].title, '普通标题')
    assert.equal(titleWaits, 0)
  })

  it('reconnect snapshots recover missed rename and terminal job in existing scopes', async () => {
    const streams: ReturnType<typeof eventStream>[] = []
    let revision = 0, snapshotCount = 0
    globalThis.fetch = (async (url, init) => {
      const path = new URL(String(url), 'http://localhost')
      if (path.pathname.endsWith('/events')) { const stream = eventStream(init!.signal!); streams.push(stream); return new Response(stream.body, { headers: { 'Content-Type': 'text/event-stream' } }) }
      if (path.pathname.endsWith('/title-snapshots')) { snapshotCount++; const { ids } = JSON.parse(String(init?.body)) as { ids: string[] }; return response({ items: ids.map(id => ({ id, title: revision ? '断线期间改名' : '新对话', title_revision: revision, job_status: revision ? 'failed' : null, job_revision: revision ? 7 : 0 })), missing_ids: [] }) }
      if (path.pathname.endsWith('/page')) return response({ items: [base('c')], total: 1 })
      if (path.pathname.endsWith('/turns')) return response({ items: [], page: 1, total_pages: 1 })
      if (path.pathname.endsWith('/approvals')) return response([])
      return response(base('c'))
    }) as typeof fetch
    const { result } = renderHook(() => ({ list: useConversations(), chat: useWorkspaceChat(completed, titled) }), { wrapper })
    await waitFor(() => assert.equal(result.current.list.items.length, 1))
    await act(async () => { await result.current.chat.select('c'); await tick() })
    const before = snapshotCount
    await act(async () => { revision = 4; streams[0].controller.close(); await tick() })
    await waitFor(() => assert.equal(result.current.chat.conversation?.title, '断线期间改名'), { timeout: 2500 })
    assert.ok(snapshotCount > before); assert.equal(streams.length, 2)
    assert.equal(result.current.list.items[0].title_job_status, 'failed')
    assert.equal(result.current.chat.conversation?.title_job_revision, 7)
  })

  it('StrictMode/unmount cancels the active connection reader and its pending snapshots', async () => {
    const streams: ReturnType<typeof eventStream>[] = [], signals: AbortSignal[] = []
    globalThis.fetch = (async (url, init) => {
      if (String(url).endsWith('/events')) { const stream = eventStream(init!.signal!); streams.push(stream); return new Response(stream.body, { headers: { 'Content-Type': 'text/event-stream' } }) }
      if (String(url).endsWith('/title-snapshots')) { signals.push(init!.signal!); return new Promise<Response>(() => {}) }
      return response({ items: [base('c')], total: 1 })
    }) as typeof fetch
    const { unmount } = renderHook(() => useConversations(), { wrapper, reactStrictMode: true })
    await waitFor(() => assert.ok(signals.length))
    assert.equal(streams.length, 2); assert.equal(streams[0].signal.aborted, true)
    await act(async () => { unmount(); await tick() })
    assert.ok(signals.every(signal => signal.aborted))
    assert.ok(streams.every(stream => stream.signal.aborted && stream.canceled() > 0 && !stream.body.locked))
  })

  it('keeps session updates with the page scope disabled and catches up when it reopens', async () => {
    const streams: ReturnType<typeof eventStream>[] = []
    let revision = 0
    globalThis.fetch = (async (url, init) => {
      const path = new URL(String(url), 'http://localhost')
      if (path.pathname.endsWith('/events')) { const stream = eventStream(init!.signal!); streams.push(stream); return new Response(stream.body, { headers: { 'Content-Type': 'text/event-stream' } }) }
      if (path.pathname.endsWith('/title-snapshots')) { const { ids } = JSON.parse(String(init?.body)) as { ids: string[] }; return response({ items: ids.map(id => ({ id, title: revision ? '后台更新' : '新对话', title_revision: revision, job_status: revision ? 'succeeded' : null, job_revision: revision ? 3 : 0 })), missing_ids: [] }) }
      if (path.pathname.endsWith('/page')) return response({ items: [base('c')], total: 1 })
      if (path.pathname.endsWith('/turns')) return response({ items: [], page: 1, total_pages: 1 })
      if (path.pathname.endsWith('/approvals')) return response([])
      return response(base('c'))
    }) as typeof fetch
    const { result, rerender } = renderHook(({ enabled }) => ({ list: useConversations(undefined, { enabled }), chat: useWorkspaceChat(completed, titled) }), { initialProps: { enabled: true }, wrapper })
    await waitFor(() => assert.equal(result.current.list.items.length, 1))
    await act(async () => { await result.current.chat.select('c') })
    rerender({ enabled: false })
    await act(async () => { revision = 2; streams[0].emit({ type: 'conversation.title.updated', conversation_id: 'c', title: '后台更新', revision: 2 }); await tick() })
    assert.equal(result.current.chat.conversation?.title, '后台更新'); assert.equal(streams.length, 1)
    assert.equal(result.current.list.items[0].title, '新对话')
    rerender({ enabled: true })
    await waitFor(() => assert.equal(result.current.list.items[0].title, '后台更新'))
    assert.equal(result.current.list.items[0].title_job_status, 'succeeded'); assert.equal(streams.length, 1)
  })

  it('malformed title payload terminates the reader and emits a safe diagnostic', async () => {
    let stream: ReturnType<typeof eventStream> | undefined, errors = 0
    globalThis.fetch = (async (_url, init) => { stream = eventStream(init!.signal!); return new Response(stream.body, { headers: { 'Content-Type': 'text/event-stream' } }) }) as typeof fetch
    renderHook(() => useTitleEvents(() => assert.fail('bad event delivered'), undefined, () => { errors++ }))
    await waitFor(() => assert.ok(stream))
    await act(async () => { stream!.controller.enqueue(new TextEncoder().encode('data: {"fake-secret"\n\n')); await tick() })
    assert.equal(errors, 1); assert.ok(stream?.signal.aborted); assert.equal(stream?.body.locked, false)
  })

  it('joins an already started reader when the ready callback fails synchronously', async () => {
    let stream: ReturnType<typeof eventStream> | undefined, errors = 0
    globalThis.fetch = (async (_url, init) => { stream = eventStream(init!.signal!); return new Response(stream.body, { headers: { 'Content-Type': 'text/event-stream' } }) }) as typeof fetch
    const view = renderHook(() => useTitleEvents(() => assert.fail('no event expected'), () => { throw new Error('safe ready fixture failure') }, () => { errors++ }))
    await waitFor(() => assert.equal(errors, 1))
    await act(async () => { await tick() })
    assert.ok(stream?.signal.aborted)
    assert.equal(stream?.canceled(), 1)
    assert.equal(stream?.body.locked, false)
    view.unmount()
  })

  it('terminates bad UTF-8, envelope and feature payloads instead of ignoring them', async () => {
    for (const bytes of [
      new Uint8Array([...new TextEncoder().encode('data: '), 0xff, 10, 10]),
      new TextEncoder().encode('data: {"code":"bad","data":{}}\n\n'),
      new TextEncoder().encode('data: {"code":100000,"data":{"type":"conversation.title.updated","conversation_id":"c","title":42}}\n\n'),
    ]) {
      let stream: ReturnType<typeof eventStream> | undefined, errors = 0
      globalThis.fetch = (async (_url, init) => { stream = eventStream(init!.signal!); return new Response(stream.body, { headers: { 'Content-Type': 'text/event-stream' } }) }) as typeof fetch
      const view = renderHook(() => useTitleEvents(() => assert.fail('bad event delivered'), undefined, () => { errors++ }))
      await waitFor(() => assert.ok(stream))
      await act(async () => { stream!.controller.enqueue(bytes); await tick() })
      assert.equal(errors, 1)
      assert.ok(stream?.signal.aborted)
      assert.equal(stream?.body.locked, false)
      view.unmount()
    }
  })

  it('measures three 801-item page/reconnect rounds and joins every title reader', { skip: process.env.KAGUYA_TITLE_RESOURCE !== '1', timeout: 60_000 }, async () => {
    const bun = (globalThis as typeof globalThis & { Bun?: { gc(force: boolean): void } }).Bun
    assert.equal(typeof bun?.gc, 'function')
    const memory = () => ({ ...process.memoryUsage(), fds: readdirSync(process.platform === 'linux' ? '/proc/self/fd' : '/dev/fd').length })
    for (let round = 1; round <= 3; round++) {
      bun!.gc(true)
      const before = memory(), observed = { ...before }
      const observe = () => { const next = memory(); for (const key of ['rss', 'heapUsed', 'external', 'fds'] as const) observed[key] = Math.max(observed[key], next[key]) }
      const counts = await (async () => {
        const streams: ReturnType<typeof eventStream>[] = []
        let revision = 1, snapshotRequests = 0, inflight = 0, peakInflight = 0
        const ids = Array.from({ length: 801 }, (_, index) => `resource-${index}`)
        globalThis.fetch = (async (url, init) => {
          const path = new URL(String(url), 'http://localhost')
          if (path.pathname.endsWith('/events')) { const stream = eventStream(init!.signal!); streams.push(stream); return new Response(stream.body, { headers: { 'Content-Type': 'text/event-stream' } }) }
          if (path.pathname.endsWith('/title-snapshots')) {
            const { ids: batch } = JSON.parse(String(init?.body)) as { ids: string[] }
            assert.ok(batch.length <= 200)
            snapshotRequests++; inflight++; peakInflight = Math.max(peakInflight, inflight)
            const current = revision
            try { await tick(); observe(); return response({ items: batch.map(id => ({ id, title: `snapshot-${current}`, title_revision: current, job_status: 'succeeded', job_revision: current })), missing_ids: [] }) } finally { inflight-- }
          }
          if (path.pathname.endsWith('/page')) {
            const page = Number(path.searchParams.get('page') ?? '1')
            // 页数据故意始终落后于事件/重连快照，不能把旧页面的标题回写。
            return response({ items: ids.slice((page - 1) * 20, page * 20).map(base), total: ids.length })
          }
          assert.fail(`unexpected title resource request ${path.pathname}`)
        }) as typeof fetch
        const view = renderHook(() => useConversations(), { wrapper })
        try {
          await waitFor(() => assert.equal(view.result.current.items.length, 20))
          for (let target = 40; target <= 820; target += 20) {
            await act(async () => { view.result.current.loadMore(); await tick() })
            await waitFor(() => assert.equal(view.result.current.items.length, Math.min(target, 801)))
            observe()
          }
          await waitFor(() => assert.ok(view.result.current.items.every(item => item.title_revision === 1)))
          await act(async () => {
            for (const id of [ids[0], ids[400], ids[800]]) streams[0].emit({ type: 'conversation.title.updated', conversation_id: id, title: 'new event', revision: 7 })
            await tick()
          })
          assert.ok([0, 400, 800].every(index => view.result.current.items[index].title_revision === 7))
          await act(async () => { revision = 8; streams[0].controller.close(); await tick() })
          await waitFor(() => assert.ok(view.result.current.items.every(item => item.title === 'snapshot-8' && item.title_revision === 8)), { timeout: 3000 })
          assert.equal(streams.length, 2)
          assert.ok(peakInflight <= 4)
          observe()
        } finally {
          await act(async () => { view.unmount(); cleanup(); await tick(); await tick() })
        }
        assert.equal(inflight, 0)
        assert.ok(streams.every(stream => stream.signal.aborted && !stream.body.locked))
        assert.equal(streams[1].canceled(), 1)
        globalThis.fetch = originalFetch
        return { snapshotRequests, peakInflight, streams: streams.length, inflight }
      })()
      bun!.gc(true)
      const after = memory()
      assert.equal(after.fds, before.fds)
      console.info('TITLE_PAGE_RESOURCE ' + JSON.stringify({ round, items: 801, ...counts, before, observed, after }))
    }
  })
})
