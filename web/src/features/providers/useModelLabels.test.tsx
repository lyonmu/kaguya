/// <reference types="node" />
import { after, afterEach, it } from 'node:test'
import assert from 'node:assert/strict'
import { Window } from 'happy-dom'

const dom = new Window({ url: 'http://localhost' })
const globals = { window: dom, document: dom.document, navigator: dom.navigator, location: dom.location, HTMLElement: dom.HTMLElement, Element: dom.Element, Node: dom.Node, IS_REACT_ACT_ENVIRONMENT: true }
const previous = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
const { renderHook, cleanup, waitFor, act } = await import('@testing-library/react')
const { useModelLabels, invalidateModelLabels } = await import('./useModelLabels')
const { modelLabelsGuard } = await import('./guards')
const originalFetch = globalThis.fetch

afterEach(async () => {
  cleanup()
  invalidateModelLabels()
  await act(async () => {})
  globalThis.fetch = originalFetch
  Object.defineProperty(globalThis, 'location', { configurable: true, writable: true, value: dom.location })
})
after(() => {
  for (const [key, descriptor] of previous) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor)
    else Reflect.deleteProperty(globalThis, key)
  }
  dom.happyDOM.abort()
})

function controlledLabels() {
  const calls: { url: URL; signal?: AbortSignal | null; resolve: (response: Response) => void; reject: (error: Error) => void }[] = []
  // 故意不服从abort，证明晚完成也不能发布或清理新slot。
  globalThis.fetch = (async (url, init) => new Promise<Response>((resolve, reject) => {
    calls.push({ url: new URL(String(url), 'http://localhost'), signal: init?.signal, resolve, reject })
  })) as typeof fetch
  return calls
}
const labels = (name: string) => [{ label: name, value: `local-${name}`, provider_id: 'provider', provider_name: 'Provider', model_id: `api-${name}`, is_default: true }]
const respond = async (call: ReturnType<typeof controlledLabels>[number], data: unknown) => {
  await act(async () => { call.resolve(Response.json({ code: 100000, data })) })
}
const fail = async (call: ReturnType<typeof controlledLabels>[number]) => {
  await act(async () => { call.reject(new TypeError('fixture network failure')) })
}

it('shares one GET, releases only its own subscription and caches only label fields', async () => {
  const calls = controlledLabels()
  const first = renderHook(() => useModelLabels())
  const second = renderHook(() => useModelLabels())
  await waitFor(() => assert.equal(calls.length, 1))
  assert.equal(calls[0].url.pathname, '/kaguya/api/v1/system/model/label')
  first.unmount()
  assert.equal(calls[0].signal?.aborted, false)
  await respond(calls[0], labels('current').map(item => ({ ...item, api_key: 'fake-secret-not-a-label' })))
  assert.deepEqual(second.result.current.models, labels('current'))
  assert.equal(second.result.current.models[0].value, 'local-current')
  const cached = renderHook(() => useModelLabels())
  assert.deepEqual(cached.result.current.models, labels('current'))
  assert.equal(calls.length, 1)
  assert.equal(cached.result.current.loading, false)
})

for (const oldResult of ['success', 'failure'] as const) {
  for (const oldFirst of [true, false]) {
    it(`keeps B owned when invalidated A ${oldResult} completes ${oldFirst ? 'before' : 'after'} B`, async () => {
      const calls = controlledLabels()
      const view = renderHook(() => useModelLabels())
      await waitFor(() => assert.equal(calls.length, 1))
      await act(async () => { invalidateModelLabels() })
      assert.equal(calls[0].signal?.aborted, true)
      await waitFor(() => assert.equal(calls.length, 2))
      const completeOld = () => oldResult === 'success' ? respond(calls[0], labels('stale')) : fail(calls[0])
      if (oldFirst) await completeOld()
      else await respond(calls[1], labels('fresh'))
      assert.equal(calls[1].signal?.aborted, false)
      // A的finally不能清B；新的消费者必须继续共享B，而不是发第3次GET。
      const another = renderHook(() => useModelLabels())
      assert.equal(calls.length, 2)
      view.unmount()
      assert.equal(calls[1].signal?.aborted, false)
      if (oldFirst) {
        assert.equal(another.result.current.loading, true)
        assert.equal(another.result.current.models.length, 0)
        await respond(calls[1], labels('fresh'))
      } else await completeOld()
      assert.deepEqual(another.result.current.models, labels('fresh'))
      assert.equal(another.result.current.error, '')
      const warmed = renderHook(() => useModelLabels())
      assert.deepEqual(warmed.result.current.models, labels('fresh'))
      assert.equal(calls.length, 2)
    })
  }
}

it('invalidates synchronously before a late result can publish during effect cleanup', async () => {
  const calls = controlledLabels()
  const observed: string[][] = []
  const view = renderHook(() => {
    const state = useModelLabels()
    observed.push(state.models.map(item => item.label))
    return state
  })
  await waitFor(() => assert.equal(calls.length, 1))
  await act(async () => {
    invalidateModelLabels()
    calls[0].resolve(Response.json({ code: 100000, data: labels('stale') }))
    await Promise.resolve()
  })
  await waitFor(() => assert.equal(calls.length, 2))
  assert.equal(observed.some(items => items.includes('stale')), false)
  await respond(calls[1], labels('fresh'))
  assert.deepEqual(view.result.current.models, labels('fresh'))
})

it('makes retry a shared new generation after failure and while a request is pending', async () => {
  const calls = controlledLabels()
  const first = renderHook(() => useModelLabels())
  const second = renderHook(() => useModelLabels())
  await waitFor(() => assert.equal(calls.length, 1))
  await fail(calls[0])
  assert.notEqual(first.result.current.error, '')
  assert.equal(first.result.current.loading, false)
  await act(async () => { first.result.current.retry() })
  await waitFor(() => assert.equal(calls.length, 2))
  assert.equal(second.result.current.loading, true)
  await act(async () => { second.result.current.retry() })
  await waitFor(() => assert.equal(calls.length, 3))
  assert.equal(calls[1].signal?.aborted, true)
  await respond(calls[1], labels('stale-retry'))
  assert.equal(first.result.current.models.length, 0)
  await respond(calls[2], labels('retried'))
  assert.deepEqual(first.result.current.models, labels('retried'))
  assert.deepEqual(second.result.current.models, labels('retried'))
  assert.equal(first.result.current.error, '')
})

it('does not reuse an aborted StrictMode slot and aborts the last live consumer', async () => {
  const calls = controlledLabels()
  const view = renderHook(() => useModelLabels(), { reactStrictMode: true })
  await waitFor(() => assert.equal(calls.length, 2))
  assert.equal(calls[0].signal?.aborted, true)
  assert.equal(calls[1].signal?.aborted, false)
  view.unmount()
  assert.equal(calls[1].signal?.aborted, true)
  await respond(calls[0], labels('old-setup'))
  await respond(calls[1], labels('unmounted'))
  const current = renderHook(() => useModelLabels())
  await waitFor(() => assert.equal(calls.length, 3))
  await respond(calls[2], labels('current'))
  assert.deepEqual(current.result.current.models, labels('current'))
})

it('separates actual Desktop API prefixes and ignores late data from the previous service', async () => {
  const location = { protocol: 'wails:', hostname: 'localhost', hash: '#api-prefix=%2Ffirst%2Fapi' }
  Object.defineProperty(globalThis, 'location', { configurable: true, writable: true, value: location })
  const calls = controlledLabels()
  const view = renderHook(() => useModelLabels())
  await waitFor(() => assert.equal(calls.length, 1))
  assert.equal(calls[0].url.pathname, '/first/api/v1/system/model/label')
  location.hash = '#api-prefix=%2Fsecond%2Fapi'
  view.rerender()
  await waitFor(() => assert.equal(calls.length, 2))
  assert.equal(calls[0].signal?.aborted, true)
  assert.equal(calls[1].url.pathname, '/second/api/v1/system/model/label')
  await respond(calls[0], labels('wrong-service'))
  assert.equal(view.result.current.models.length, 0)
  await respond(calls[1], labels('right-service'))
  assert.deepEqual(view.result.current.models, labels('right-service'))
  // 已完成缓存也不能跨服务复用。
  location.hash = '#api-prefix=%2Fthird%2Fapi'
  view.rerender()
  assert.equal(view.result.current.models.length, 0)
  await waitFor(() => assert.equal(calls.length, 3))
  assert.equal(calls[2].url.pathname, '/third/api/v1/system/model/label')
  await respond(calls[2], labels('third-service'))
  assert.deepEqual(view.result.current.models, labels('third-service'))
})

it('reports invalid Desktop prefixes without fallback requests and recovers on a valid prefix', async () => {
  const location = { protocol: 'wails:', hostname: 'localhost', hash: '#api-prefix=https%3A%2F%2Fexample.test' }
  Object.defineProperty(globalThis, 'location', { configurable: true, writable: true, value: location })
  const calls = controlledLabels()
  const view = renderHook(() => useModelLabels())
  assert.equal(calls.length, 0)
  assert.match(view.result.current.error, /本地路径/)
  assert.equal(view.result.current.loading, false)
  location.hash = '#api-prefix=%2Fkaguya%2Fapi'
  view.rerender()
  await waitFor(() => assert.equal(calls.length, 1))
  await respond(calls[0], null)
  assert.equal(view.result.current.models.length, 0)
  assert.equal(view.result.current.error, '')
  const cached = renderHook(() => useModelLabels())
  assert.equal(cached.result.current.models.length, 0)
  assert.equal(calls.length, 1)
})

it('rejects invalid label payloads instead of caching them and retries with validated fields', async () => {
  for (const input of [undefined, {}, [null], [{ ...labels('invalid')[0], is_default: 'yes' }]]) assert.equal(modelLabelsGuard(input).ok, false)
  const calls = controlledLabels()
  const view = renderHook(() => useModelLabels())
  await waitFor(() => assert.equal(calls.length, 1))
  await respond(calls[0], { api_key: 'fake-secret' })
  assert.equal(view.result.current.error, '模型标签格式不正确')
  assert.equal(view.result.current.models.length, 0)
  await act(async () => view.result.current.retry())
  await waitFor(() => assert.equal(calls.length, 2))
  await respond(calls[1], labels('valid'))
  assert.deepEqual(view.result.current.models, labels('valid'))
})
