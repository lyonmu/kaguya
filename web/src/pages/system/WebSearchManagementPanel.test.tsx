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
const { render, fireEvent, cleanup, waitFor, act } = await import('@testing-library/react')
const { App } = await import('antd')
const { WebSearchManagementPanel } = await import('./WebSearchManagementPanel')
const originalFetch = globalThis.fetch
const response = (data: unknown) => Response.json({ code: 100000, data })
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

it('requires the initial key and uses one switch for both tools', async () => {
  let config = { enabled: false, key_configured: false }
  const updates: unknown[] = []
  globalThis.fetch = (async (url, init) => {
    assert.equal(String(url), '/kaguya/api/v1/system/web-search')
    if (init?.method === 'PUT') {
      const body = JSON.parse(String(init.body))
      updates.push(body)
      config = { enabled: body.enabled, key_configured: config.key_configured || Boolean(body.api_key) }
    }
    return response(config)
  }) as typeof fetch

  const view = render(<App><WebSearchManagementPanel /></App>)
  const toggle = await view.findByRole('switch', { name: '启用联网搜索和抓取' })
  assert.equal(toggle.getAttribute('aria-checked'), 'false')
  const submit = view.getByRole('button', { name: /保存配置/ })
  fireEvent.click(toggle)
  fireEvent.click(submit)
  await view.findByText('启用前请填写 DeepSeek API Key')
  assert.equal(updates.length, 0)

  const input = view.getByLabelText('DeepSeek API Key') as HTMLInputElement
  fireEvent.change(input, { target: { value: 'test-key' } })
  fireEvent.click(submit)
  await waitFor(() => assert.equal(input.value, ''))
  assert.deepEqual(updates, [{ enabled: true, api_key: 'test-key' }])
  assert.ok(view.getByPlaceholderText('已配置，留空保留现有密钥'))

  fireEvent.click(toggle)
  fireEvent.click(submit)
  await waitFor(() => assert.deepEqual(updates[1], { enabled: false }))
  assert.ok(view.getByPlaceholderText('已配置，留空保留现有密钥'))
})

it('shows a load error and reloads the saved state on retry', async () => {
  let loads = 0
  globalThis.fetch = (async () => {
    if (++loads === 1) return Response.json({ code: 104000, message: '配置读取失败' })
    return response({ enabled: true, key_configured: true })
  }) as typeof fetch
  const view = render(<App><WebSearchManagementPanel /></App>)
  assert.ok(await view.findByText('配置读取失败'))
  fireEvent.click(view.getByRole('button', { name: /重.*试/ }))
  const toggle = await view.findByRole('switch', { name: '启用联网搜索和抓取' })
  assert.equal(toggle.getAttribute('aria-checked'), 'true')
  assert.ok(view.getByPlaceholderText('已配置，留空保留现有密钥'))
})
