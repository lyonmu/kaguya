/// <reference types="node" />
import { after, afterEach, it } from 'node:test'
import assert from 'node:assert/strict'
import { Window } from 'happy-dom'
import type { AIProvider } from '../../features/providers/types'

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
const { ProviderManagementPage } = await import('./ProviderManagementPage')
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

// 后端只会返回掩码，测试据此确认页面不再自行拼装或回填明文。
const provider: AIProvider = {
  id: 'p1', provider_name: '示例提供商', provider_type: 'normal', api_protocol: 'openai-chat',
  api_key: 'sk-l••••7890', api_key_set: true, base_url: 'https://api.example.com/v1/chat/completions',
  models: [], created_at: '2026-09-10T00:00:00Z', updated_at: '2026-09-10T00:00:00Z',
}

it('lists the backend mask and requests the plaintext only when revealed', async () => {
  const paths: string[] = []
  globalThis.fetch = (async (url: RequestInfo | URL) => {
    const path = new URL(String(url), 'http://localhost').pathname
    paths.push(path)
    if (path.endsWith('/api-key')) return response({ id: 'p1', api_key: 'sk-live-full-value' })
    if (path.endsWith('/page')) return response({ total: 1, items: [provider], page: 1, page_size: 10 })
    return response([])
  }) as typeof fetch

  const view = render(<App><ProviderManagementPage /></App>)
  await waitFor(() => assert.ok(view.getByText('示例提供商')))
  // 列表阶段不应触碰明文接口。
  assert.ok(!paths.some(path => path.endsWith('/api-key')), 'api-key must not be fetched with the list')
  assert.ok(view.getByText('sk-l••••7890'))
  assert.equal(view.queryByText('sk-live-full-value'), null)

  await act(async () => {
    fireEvent.click(view.getByLabelText('查看 示例提供商 的 API Key'))
  })
  await waitFor(() => assert.ok(view.getByText('sk-live-full-value')))
  assert.ok(paths.some(path => path.endsWith('/provider/p1/api-key')), 'reveal must call the dedicated endpoint')
})

it('keeps the stored key when editing without entering a new one', async () => {
  const bodies: string[] = []
  globalThis.fetch = (async (url: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(url), 'http://localhost').pathname
    if (path.endsWith('/page')) return response({ total: 1, items: [provider], page: 1, page_size: 10 })
    if (init?.method === 'PUT') {
      bodies.push(String(init.body))
      return response(provider)
    }
    return response([])
  }) as typeof fetch

  const view = render(<App><ProviderManagementPage /></App>)
  await waitFor(() => assert.ok(view.getByText('示例提供商')))
  await act(async () => {
    fireEvent.click(view.getByText('编辑'))
  })
  // 表单不能预填掩码，否则保存时会把掩码当成新密钥写回。
  // Modal 渲染在 portal 中，因此从 baseElement 查找。
  await waitFor(() => assert.ok(view.baseElement.querySelector('input#api_key'), 'api key input must exist'))
  const input = view.baseElement.querySelector<HTMLInputElement>('input#api_key')
  assert.equal(input?.value, '')

  await act(async () => {
    // antd 会在两个汉字的按钮文本中插入空格，因此按 DOM 位置而非文本来定位确认按钮。
    fireEvent.click(view.baseElement.querySelector<HTMLButtonElement>('.ant-modal-footer .ant-btn-primary')!)
  })
  await waitFor(() => assert.equal(bodies.length, 1))
  assert.equal(JSON.parse(bodies[0]).api_key, '', 'untouched api_key must be submitted empty')
})
