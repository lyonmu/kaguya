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
  cancelAnimationFrame: dom.cancelAnimationFrame.bind(dom),
  getComputedStyle: dom.getComputedStyle.bind(dom),
  requestAnimationFrame: dom.requestAnimationFrame.bind(dom),
  IS_REACT_ACT_ENVIRONMENT: true,
}
const previous = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
// diff 视图库用 canvas 测量文本宽度，happy-dom 不提供 2d context。
dom.HTMLCanvasElement.prototype.getContext = (() => ({ font: '', measureText: (text: string) => ({ width: text.length * 7 }) })) as never
const { render, fireEvent, cleanup, waitFor, act, within } = await import('@testing-library/react')
const { App } = await import('antd')
const { CodeBrowserDrawer } = await import('./components/CodeBrowserDrawer')

const originalFetch = globalThis.fetch
const response = (data: unknown) => Response.json({ code: 100000, data })
const diff = 'diff --git a/main.go b/main.go\nindex 000..111 100644\n--- a/main.go\n+++ b/main.go\n@@ -1 +1,2 @@\n package main\n+// hi\n'
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

it('loads the file tree with git badges and renders diff and file views on demand', async () => {
  const requests: string[] = []
  globalThis.fetch = (async (url: string) => {
    const value = String(url)
    requests.push(value)
    if (value.includes('/tree')) return response({ name: 'repo', path: '', items: [{ name: 'main.go', path: 'main.go', is_dir: false, size: 20 }], truncated: false })
    if (value.includes('/git/status')) return response({ is_git: true, branch: 'main', head: 'abc1234', files: [{ path: 'main.go', status: 'modified', staged: false, additions: 1, deletions: 0 }], truncated: false })
    if (value.includes('/git/diff')) return response({ path: 'main.go', status: 'modified', binary: false, truncated: false, diff })
    if (value.includes('/content')) return response({ path: 'main.go', size: 20, binary: false, truncated: false, content: 'package main\n' })
    return response({})
  }) as typeof fetch
  const view = render(<App><CodeBrowserDrawer open projectId="1" projectName="repo" onClose={() => {}} /></App>)
  assert.ok(view.container)
  const panel = within(document.body)
  await waitFor(() => assert.ok(panel.getByText('main.go')))
  assert.ok(panel.getByText('M'))
  const label = panel.getByText('main.go')
  fireEvent.click(label.closest('.ant-tree-node-content-wrapper') ?? label)
  await waitFor(() => assert.ok(dom.document.querySelector('.code-diff')), { timeout: 4000 })
  assert.ok(requests.some(url => url.includes('/git/diff')))
  fireEvent.click(panel.getByText('文件'))
  await waitFor(() => assert.ok(dom.document.querySelector('.code-lines')), { timeout: 4000 })
  assert.ok((dom.document.querySelector('.code-line-text')?.textContent ?? '').includes('package main'))
})
