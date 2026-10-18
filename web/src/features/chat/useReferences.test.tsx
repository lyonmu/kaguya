/// <reference types="node" />
import { after, afterEach, it } from 'node:test'
import assert from 'node:assert/strict'
import { Window } from 'happy-dom'

const dom = new Window({ url: 'http://localhost' })
const globals = { window: dom, document: dom.document, navigator: dom.navigator, HTMLElement: dom.HTMLElement, Element: dom.Element, Node: dom.Node, IS_REACT_ACT_ENVIRONMENT: true }
const previous = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
const { renderHook, cleanup, waitFor, act } = await import('@testing-library/react')
const { useReferences } = await import('./useReferences')
const originalFetch = globalThis.fetch

afterEach(() => { cleanup(); globalThis.fetch = originalFetch })
after(() => {
  for (const [key, descriptor] of previous) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor)
    else Reflect.deleteProperty(globalThis, key)
  }
  dom.happyDOM.abort()
})

function controlledSearch() {
  const calls: { url: URL; signal?: AbortSignal | null; resolve: (response: Response) => void }[] = []
  globalThis.fetch = (async (url, init) => new Promise<Response>(resolve => {
    calls.push({ url: new URL(String(url), 'http://localhost'), signal: init?.signal, resolve })
  })) as typeof fetch
  return calls
}
const page = (names: string[], nextCursor = '', extra: Record<string, unknown> = {}) => ({ files: names, items: names.map(path => ({ type: 'file', path })), truncated: !!nextCursor, next_cursor: nextCursor, scan_incomplete: false, ...extra })
const respond = async (call: ReturnType<typeof controlledSearch>[number], data: unknown) => {
  await act(async () => { call.resolve(Response.json({ code: 100000, data })) })
}

it('loads bounded subsequent pages, deduplicates type/path and owns one in-flight request', async () => {
  const calls = controlledSearch()
  const view = renderHook(() => useReferences('project-a', 'src', true))
  await waitFor(() => assert.equal(calls.length, 1))
  assert.equal(calls[0].url.searchParams.get('include_directories'), 'true')
  await respond(calls[0], page(['src/a'], 'next-a'))
  await act(async () => { view.result.current.loadMore(); view.result.current.loadMore() })
  assert.equal(calls.length, 2)
  assert.equal(calls[1].url.searchParams.get('cursor'), 'next-a')
  assert.equal(view.result.current.loadingMore, true)
  await respond(calls[1], page(['src/a', 'src/b'], '', { items: [{ type: 'file', path: 'src/a' }, { type: 'file', path: 'src/b' }, { type: 'directory', path: 'src/a' }] }))
  assert.deepEqual(view.result.current.items.map(item => [item.type, item.path]), [['file', 'src/a'], ['file', 'src/b'], ['directory', 'src/a']])
  assert.equal(view.result.current.loadingMore, false)
  assert.equal(view.result.current.nextCursor, '')
  await act(async () => view.result.current.loadMore())
  assert.equal(calls.length, 2)
})

it('cancels stale project/query generations and ignores their late success and finally', async () => {
  const calls = controlledSearch()
  const view = renderHook(({ project, query }) => useReferences(project, query, true), { initialProps: { project: 'a', query: 'first' } })
  await waitFor(() => assert.equal(calls.length, 1))
  view.rerender({ project: 'b', query: 'second' })
  assert.equal(calls[0].signal?.aborted, true)
  assert.equal(view.result.current.items.length, 0)
  await waitFor(() => assert.equal(calls.length, 2))
  await respond(calls[0], page(['stale'], 'stale-cursor'))
  assert.equal(view.result.current.items.length, 0)
  assert.equal(view.result.current.loading, true)
  await respond(calls[1], page(['current']))
  assert.deepEqual(view.result.current.items.map(item => item.path), ['current'])
  assert.equal(view.result.current.error, '')
})

it('aborts loadMore when a mention closes and prevents late data from returning', async () => {
  const calls = controlledSearch()
  const view = renderHook(({ enabled }) => useReferences('a', '', enabled), { initialProps: { enabled: true } })
  await waitFor(() => assert.equal(calls.length, 1))
  await respond(calls[0], page(['a'], 'next'))
  await act(async () => view.result.current.loadMore())
  view.rerender({ enabled: false })
  assert.equal(calls[1].signal?.aborted, true)
  await respond(calls[1], page(['late']))
  assert.equal(view.result.current.items.length, 0)
  assert.equal(view.result.current.loadingMore, false)
  assert.equal(view.result.current.nextCursor, '')
})

it('replaces pages on cursor_reset and accepts partial terminal results without inventing a page', async () => {
  const calls = controlledSearch()
  const view = renderHook(() => useReferences('a', '', true))
  await waitFor(() => assert.equal(calls.length, 1))
  await respond(calls[0], page(['old'], 'old-next'))
  await act(async () => view.result.current.loadMore())
  await respond(calls[1], page(['new'], 'new-next', { cursor_reset: true }))
  assert.deepEqual(view.result.current.items.map(item => item.path), ['new'])
  await act(async () => view.result.current.loadMore())
  assert.equal(calls[2].url.searchParams.get('cursor'), 'new-next')
  await respond(calls[2], page(['last'], '', { truncated: true, scan_incomplete: true }))
  assert.deepEqual(view.result.current.items.map(item => item.path), ['new', 'last'])
  assert.equal(view.result.current.scanIncomplete, true)
  await act(async () => view.result.current.loadMore())
  assert.equal(calls.length, 3)
})

it('stops identical or cyclic cursors with a fixed error and retains confirmed candidates', async () => {
  for (const cycle of [false, true]) {
    const calls = controlledSearch()
    const view = renderHook(() => useReferences('a', '', true))
    await waitFor(() => assert.equal(calls.length, 1))
    await respond(calls[0], page(['a'], 'next-a'))
    await act(async () => view.result.current.loadMore())
    if (cycle) {
      await respond(calls[1], page(['b'], 'next-b'))
      await act(async () => view.result.current.loadMore())
    }
    const last = calls.at(-1)
    assert.ok(last)
    await respond(last, page(['should-not-append'], 'next-a'))
    assert.equal(view.result.current.error, '文件搜索游标未推进，请重新搜索')
    assert.equal(view.result.current.nextCursor, '')
    assert.equal(view.result.current.items.some(item => item.path === 'should-not-append'), false)
    const count = calls.length
    await act(async () => view.result.current.loadMore())
    assert.equal(calls.length, count)
    view.unmount()
  }
})

it('keeps loaded pages on pagination protocol failure and retries the same unconsumed cursor explicitly', async () => {
  const calls = controlledSearch()
  const view = renderHook(() => useReferences('a', '', true))
  await waitFor(() => assert.equal(calls.length, 1))
  await respond(calls[0], page(['kept'], 'retry-next'))
  await act(async () => view.result.current.loadMore())
  await respond(calls[1], page(['invalid'], '', { scan_incomplete: 'bad' }))
  assert.deepEqual(view.result.current.items.map(item => item.path), ['kept'])
  assert.match(view.result.current.error, /文件引用格式不正确/)
  assert.equal(view.result.current.loadingMore, false)
  await act(async () => view.result.current.loadMore())
  assert.equal(calls[2].url.searchParams.get('cursor'), 'retry-next')
  await respond(calls[2], page(['recovered']))
  assert.equal(view.result.current.error, '')
  assert.deepEqual(view.result.current.items.map(item => item.path), ['kept', 'recovered'])
})

it('supports old files-only responses and cancels every live request on StrictMode unmount', async () => {
  const calls = controlledSearch()
  const view = renderHook(() => useReferences('a', '', true), { reactStrictMode: true })
  await waitFor(() => assert.equal(calls.length, 1))
  await respond(calls[0], { files: ['legacy'], truncated: true })
  assert.deepEqual(view.result.current.items, [{ type: 'file', path: 'legacy' }])
  assert.equal(view.result.current.scanIncomplete, false)
  assert.equal(view.result.current.nextCursor, '')
  view.unmount()
  const another = renderHook(() => useReferences('a', '', true), { reactStrictMode: true })
  await waitFor(() => assert.equal(calls.length, 2))
  another.unmount()
  assert.equal(calls[1].signal?.aborted, true)
})
