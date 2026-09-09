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
  getComputedStyle: dom.getComputedStyle.bind(dom),
  IS_REACT_ACT_ENVIRONMENT: true,
}
const previous = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
const { render, fireEvent, cleanup, waitFor, act } = await import('@testing-library/react')
const { App } = await import('antd')
const { ChatPage } = await import('./ChatPage')
const { AppLayout } = await import('../../components/layout/AppLayout')
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

it('hides runtime details, toggles the conversation panel and offers model selection', async () => {
  globalThis.fetch = (async url => {
    if (String(url).includes('/model/label')) return response([{ label: '模型 A', value: 'local-a', provider_name: '提供商 A', provider_id: 'p', model_id: 'api-a' }])
    return response({ items: [], total: 0 })
  }) as typeof fetch
  const view = render(<App><ChatPage /></App>)
  assert.equal(view.queryByText('运行详情'), null)
  assert.ok(view.container.querySelector('.chat-sidebar'))
  fireEvent.click(view.getByLabelText('收起会话列表'))
  assert.equal(view.container.querySelector('.chat-sidebar'), null)
  fireEvent.click(view.getByLabelText('展开会话列表'))
  assert.ok(view.container.querySelector('.chat-sidebar'))
  const selector = view.getByLabelText('对话模型')
  fireEvent.mouseDown(selector.closest('.ant-select')!.querySelector('.ant-select-selector') ?? selector)
  await waitFor(() => assert.ok(view.getByText('提供商 A / 模型 A')))
  fireEvent.click(view.getByText('提供商 A / 模型 A'))
  await waitFor(() => assert.ok(selector.closest('.ant-select')?.textContent?.includes('提供商 A / 模型 A')))
})

it('opens AI providers when entering system settings', () => {
  let page = ''
  const view = render(<AppLayout colorMode="light" currentPage="chat" onPageChange={value => { page = value }} onToggleColorMode={() => {}} />)
  fireEvent.click(view.getByLabelText('系统管理'))
  assert.equal(page, 'ai-providers')
})
