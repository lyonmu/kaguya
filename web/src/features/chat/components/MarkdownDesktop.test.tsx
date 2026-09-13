/// <reference types="node" />
import { after, afterEach, it } from 'node:test'
import assert from 'node:assert/strict'
import { Window } from 'happy-dom'

// Desktop 原生 scheme：复制与外部链接必须走宿主窄接口，而不是浏览器 API。
const dom = new Window({ url: 'wails://localhost/#api-prefix=%2Fkaguya%2Fapi' })
const globals = {
  window: dom, document: dom.document, navigator: dom.navigator, location: dom.location,
  HTMLElement: dom.HTMLElement, Element: dom.Element, Node: dom.Node,
  SVGElement: dom.SVGElement, ShadowRoot: dom.ShadowRoot,
  CSSStyleSheet: dom.CSSStyleSheet,
  DOMParser: dom.DOMParser, XMLSerializer: dom.XMLSerializer,
  MutationObserver: dom.MutationObserver, ResizeObserver: dom.ResizeObserver,
  cancelAnimationFrame: dom.cancelAnimationFrame.bind(dom),
  getComputedStyle: dom.getComputedStyle.bind(dom), requestAnimationFrame: dom.requestAnimationFrame.bind(dom), IS_REACT_ACT_ENVIRONMENT: true,
}
const previous = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
const { render, cleanup, act, fireEvent } = await import('@testing-library/react')
const { Markdown } = await import('./Markdown')

const originalFetch = globalThis.fetch
afterEach(() => {
  cleanup()
  globalThis.fetch = originalFetch
})
after(async () => {
  await dom.happyDOM.close()
  for (const [key, descriptor] of previous) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor)
    else Reflect.deleteProperty(globalThis, key)
  }
})

function mockNative(): Array<{ url: string; body: unknown }> {
  const calls: Array<{ url: string; body: unknown }> = []
  globalThis.fetch = (async (url, init) => {
    calls.push({ url: String(url), body: init?.body === undefined ? undefined : JSON.parse(String(init.body)) })
    return Response.json({ ok: true })
  }) as typeof fetch
  return calls
}

it('copies through the native clipboard endpoint', async () => {
  const calls = mockNative()
  const view = render(<Markdown text={'```go\nfmt.Println(1)\n```'} />)
  await act(async () => { fireEvent.click(view.getByLabelText('复制代码')) })
  assert.deepEqual(calls, [{ url: '/__desktop/clipboard', body: { text: 'fmt.Println(1)' } }])
  assert.ok(view.getByText('已复制'))
})

it('opens external links with the system browser instead of navigating', async () => {
  const calls = mockNative()
  const view = render(<Markdown text="[文档](https://example.com/guide)" />)
  const link = view.getByRole('link', { name: '文档' })
  // 在 document 上监听气泡阶段，晚于 React 根容器处理，可读取最终 defaultPrevented。
  let prevented: boolean | undefined
  dom.document.addEventListener('click', event => { prevented = event.defaultPrevented }, { once: true })
  await act(async () => { fireEvent.click(link) })
  assert.deepEqual(calls, [{ url: '/__desktop/open-external', body: { url: 'https://example.com/guide' } }])
  assert.equal(prevented, true, 'external navigation was not prevented')
  assert.ok(dom.location.href.startsWith('wails://localhost/'), `unexpected navigation to ${dom.location.href}`)
  assert.equal(link.getAttribute('target'), '_blank')
})

it('reports a failed native system operation', async () => {
  globalThis.fetch = (async () => new Response(JSON.stringify({ error: 'clipboard write failed' }), { status: 500 })) as typeof fetch
  const view = render(<Markdown text={'```go\nx\n```'} />)
  await act(async () => { fireEvent.click(view.getByLabelText('复制代码')) })
  assert.ok(view.getByText('复制失败'))
})
