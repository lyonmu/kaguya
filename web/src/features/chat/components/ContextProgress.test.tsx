/// <reference types="node" />
import { after, afterEach, it } from 'node:test'
import assert from 'node:assert/strict'
import { Window } from 'happy-dom'

const dom = new Window({ url: 'http://localhost' })
const globals = {
  window: dom, document: dom.document, navigator: dom.navigator,
  HTMLElement: dom.HTMLElement, Element: dom.Element, Node: dom.Node,
  SVGElement: dom.SVGElement, ShadowRoot: dom.ShadowRoot,
  MutationObserver: dom.MutationObserver, ResizeObserver: dom.ResizeObserver,
  getComputedStyle: dom.getComputedStyle.bind(dom), IS_REACT_ACT_ENVIRONMENT: true,
}
const previous = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
const { render, cleanup, waitFor, act } = await import('@testing-library/react')
const { ContextProgress } = await import('./ContextProgress')
const originalFetch = globalThis.fetch
const response = (id: string, percent: number | null) => Response.json({ code: 100000, data: { conversation_id: id, model_name: id, context_tokens: 450, effective_window: 900, context_window: 1000, percent, max_window_percent: 45 } })
afterEach(async () => {
  cleanup()
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
  globalThis.fetch = originalFetch
})
after(async () => {
  await dom.happyDOM.close()
  for (const [key, descriptor] of previous) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor)
    else Reflect.deleteProperty(globalThis, key)
  }
})

it('loads on completion, refreshes each turn and clears on new conversation', async () => {
  let calls = 0
  globalThis.fetch = (async () => response('a', ++calls * 50)) as typeof fetch
  const view = render(<ContextProgress />)
  assert.equal(calls, 0)
  view.rerender(<ContextProgress conversationId="a" turnCount={1} />)
  await waitFor(() => assert.ok(view.getByLabelText('上下文占用 50.0%')))
  view.rerender(<ContextProgress conversationId="a" turnCount={2} />)
  await waitFor(() => assert.ok(view.getByLabelText('上下文占用 100.0%')))
  view.rerender(<ContextProgress />)
  await waitFor(() => assert.ok(view.getByLabelText('上下文占用未知')))
  assert.equal(calls, 2)
})

it('ignores a stale response after switching conversations and preserves unknown usage', async () => {
  let resolveFirst: (value: Response) => void = () => {}
  globalThis.fetch = (async url => String(url).includes('/a/') ? new Promise<Response>(resolve => { resolveFirst = resolve }) : response('b', null)) as typeof fetch
  const view = render(<ContextProgress conversationId="a" turnCount={1} />)
  view.rerender(<ContextProgress conversationId="b" turnCount={1} />)
  await waitFor(() => assert.ok(view.getByLabelText('上下文占用未知')))
  await act(async () => { resolveFirst(response('a', 99)); await new Promise(resolve => setTimeout(resolve, 0)) })
  assert.equal(view.queryByLabelText('上下文占用 99.0%'), null)
})
