/// <reference types="node" />
import { after, afterEach, it } from 'node:test'
import assert from 'node:assert/strict'
import { Window } from 'happy-dom'
import type { Project } from './types'

const dom = new Window({ url: 'http://localhost' })
const globals = { window: dom, document: dom.document, navigator: dom.navigator, HTMLElement: dom.HTMLElement, Element: dom.Element, Node: dom.Node, IS_REACT_ACT_ENVIRONMENT: true }
const previous = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
const { renderHook, cleanup, act } = await import('@testing-library/react')
const { useProjects } = await import('./useProjects')
const { useProjectDirectories } = await import('./useProjectDirectories')
const originalFetch = globalThis.fetch
afterEach(async () => { cleanup(); await act(async () => {}); globalThis.fetch = originalFetch })
after(() => {
  for (const [key, descriptor] of previous) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor)
    else Reflect.deleteProperty(globalThis, key)
  }
  dom.happyDOM.abort()
})
const envelope = (data: unknown) => Response.json({ code: 100000, data })
const project = (id: string): Project => ({ id, name: id, path: `/root/${id}`, description: '', created_at: '' })
function controlledRequests() {
  const calls: { signal?: AbortSignal | null; method: string; resolve: (response: Response) => void }[] = []
  globalThis.fetch = (async (url, init) => {
    if (String(url).includes('/page?')) return envelope({ items: [], total: 0, page: 1, page_size: 20 })
    return new Promise<Response>(resolve => { calls.push({ signal: init?.signal, method: init?.method ?? 'GET', resolve }) })
  }) as typeof fetch
  return calls
}

it('owns only the latest detail and cancels detail on unmount', async () => {
  const calls = controlledRequests()
  const view = renderHook(() => useProjects())
  let first!: Promise<Project | undefined>, second!: Promise<Project | undefined>
  act(() => { first = view.result.current.loadDetail('a') })
  act(() => { second = view.result.current.loadDetail('b') })
  assert.equal(calls[0].signal?.aborted, true)
  await act(async () => { calls[0].resolve(envelope(project('a'))); assert.equal(await first, undefined) })
  assert.equal(view.result.current.saving, true, 'stale finally must not clear the new owner')
  await act(async () => { calls[1].resolve(envelope(project('b'))); assert.equal((await second)?.id, 'b') })
  assert.equal(view.result.current.saving, false)
  act(() => { first = view.result.current.loadDetail('c') })
  view.unmount()
  assert.equal(calls[2].signal?.aborted, true)
  await act(async () => { calls[2].resolve(envelope(project('c'))); assert.equal(await first, undefined) })
})

it('serializes mutations, refreshes only committed results and retains state on failure', async () => {
  const calls = controlledRequests()
  const view = renderHook(() => useProjects())
  const input = { name: 'saved', path: '/root/saved', description: '' }
  let saving!: Promise<Project | undefined>
  act(() => { saving = view.result.current.save('', input) })
  assert.equal(view.result.current.saving, true)
  await act(async () => { assert.equal(await view.result.current.save('', input), undefined) })
  await act(async () => { assert.equal(await view.result.current.loadDetail('other'), undefined) })
  assert.equal(calls.length, 1)
  assert.equal(calls[0].signal?.aborted, false, 'detail must not cancel an in-flight mutation')
  await act(async () => { calls[0].resolve(envelope(project('saved'))); assert.equal((await saving)?.id, 'saved') })
  assert.equal(view.result.current.revision, 1)
  assert.equal(view.result.current.saving, false)
  let removal!: Promise<boolean>
  act(() => { removal = view.result.current.remove('saved') })
  await act(async () => {
    calls[1].resolve(Response.json({ code: 105000, message: 'fixture delete failed' }))
    await assert.rejects(removal, /fixture delete failed/)
  })
  assert.equal(view.result.current.revision, 1)
  assert.equal(view.result.current.saving, false)
  act(() => { removal = view.result.current.remove('saved') })
  await act(async () => { calls[2].resolve(envelope(null)); assert.equal(await removal, true) })
  assert.equal(view.result.current.revision, 2)
  assert.deepEqual(calls.map(call => call.method), ['POST', 'DELETE', 'DELETE'])
})

it('does not publish canceled save results or refresh after unmount', async () => {
  const calls = controlledRequests()
  const view = renderHook(() => useProjects())
  let saving!: Promise<Project | undefined>
  act(() => { saving = view.result.current.save('saved', { name: 'saved', path: '/root/saved', description: '' }) })
  act(() => { view.result.current.cancelOperation() })
  assert.equal(calls[0].signal?.aborted, true)
  await act(async () => { calls[0].resolve(envelope(project('saved'))); assert.equal(await saving, undefined) })
  assert.equal(view.result.current.revision, 0)
  act(() => { saving = view.result.current.save('saved', { name: 'saved', path: '/root/saved', description: '' }) })
  view.unmount()
  assert.equal(calls[1].signal?.aborted, true)
  await act(async () => { calls[1].resolve(envelope(project('saved'))); assert.equal(await saving, undefined) })
})

it('ignores late directory responses across navigation, close and unmount', async () => {
  const calls = controlledRequests()
  const view = renderHook(() => useProjectDirectories())
  const directories = (path: string) => ({ home: '/root', path, parent: '/root', items: [] })
  let first!: ReturnType<typeof view.result.current.browse>, second!: typeof first
  act(() => { first = view.result.current.browse('/root/a') })
  act(() => { second = view.result.current.browse('/root/b') })
  assert.equal(calls[0].signal?.aborted, true)
  await act(async () => { calls[0].resolve(envelope(directories('/root/a'))); assert.equal(await first, undefined) })
  assert.equal(view.result.current.browsing, true)
  await act(async () => { calls[1].resolve(envelope(directories('/root/b'))); assert.equal((await second)?.path, '/root/b') })
  assert.equal(view.result.current.directories?.path, '/root/b')
  act(() => { first = view.result.current.browse('/root/c'); view.result.current.cancel() })
  assert.equal(calls[2].signal?.aborted, true)
  await act(async () => { calls[2].resolve(envelope(directories('/root/c'))); assert.equal(await first, undefined) })
  assert.equal(view.result.current.directories, undefined)
  assert.equal(view.result.current.browsing, false)
  act(() => { first = view.result.current.browse('/root/d') })
  view.unmount()
  assert.equal(calls[3].signal?.aborted, true)
  await act(async () => { calls[3].resolve(envelope(directories('/root/d'))); assert.equal(await first, undefined) })
})

it('reports directory failure and recovers without reusing failed data', async () => {
  const calls = controlledRequests()
  const view = renderHook(() => useProjectDirectories())
  let loading!: ReturnType<typeof view.result.current.browse>
  act(() => { loading = view.result.current.browse('/root/no') })
  await act(async () => { calls[0].resolve(Response.json({ code: 105000, message: 'fixture directory failed' })); await loading })
  assert.equal(view.result.current.directoryError, 'fixture directory failed')
  assert.equal(view.result.current.browsing, false)
  act(() => { loading = view.result.current.browse('/root/yes') })
  await act(async () => { calls[1].resolve(envelope({ home: '/root', path: '/root/yes', parent: '/root', items: [] })); await loading })
  assert.equal(view.result.current.directoryError, '')
  assert.equal(view.result.current.directories?.path, '/root/yes')
})
