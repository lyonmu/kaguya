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
const { render, fireEvent, cleanup, waitFor, act, within } = await import('@testing-library/react')
const { App } = await import('antd')
const { ChatPage } = await import('./ChatPage')
const { AppLayout } = await import('../../components/layout/AppLayout')
const { VirtualList } = await import('../../features/chat/components/VirtualList')
const { MessageList } = await import('../../features/chat/components/MessageList')
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
  fireEvent.click(await view.findByText('提供商 A'))
  fireEvent.click(await view.findByText('模型 A'))
  await waitFor(() => assert.ok(selector.closest('.ant-select')?.textContent?.includes('提供商 A / 模型 A')))
})

it('groups navigation with refresh and keeps it accessible after collapsing the conversation list', async () => {
  globalThis.fetch = (async url => response(String(url).includes('/model/label') ? [] : { items: [], total: 0 })) as typeof fetch
  let page = ''
  let toggles = 0
  const view = render(<App><AppLayout colorMode="light" currentPage="chat" onPageChange={value => { page = value }} onToggleColorMode={() => { toggles++ }}><ChatPage /></AppLayout></App>)
  assert.equal(view.container.querySelector('.ant-layout-sider'), null)
  assert.equal(view.queryByLabelText('Console ready'), null)
  assert.equal(view.queryByText('K'), null)
  const footer = within(view.container.querySelector('.chat-sidebar-footer')! as HTMLElement)
  for (const name of ['Kaguya', '对话管理', '系统管理', '切换颜色模式', '刷新列表']) assert.ok(footer.getByRole('button', { name }))
  fireEvent.click(footer.getByLabelText('系统管理'))
  assert.equal(page, 'ai-providers')
  fireEvent.click(footer.getByLabelText('切换颜色模式'))
  assert.equal(toggles, 1)
  fireEvent.click(footer.getByLabelText('收起快捷操作'))
  assert.equal(footer.queryByLabelText('刷新列表'), null)
  assert.equal(footer.getByLabelText('展开快捷操作').getAttribute('aria-expanded'), 'false')
  fireEvent.click(view.getByLabelText('收起会话列表'))
  const bottom = within(view.container.querySelector('.chat-bottom-actions')! as HTMLElement)
  fireEvent.click(bottom.getByLabelText('展开快捷操作'))
  fireEvent.click(bottom.getByLabelText('对话管理'))
  assert.equal(page, 'chat')
  await waitFor(() => assert.equal(bottom.getByLabelText('刷新列表').hasAttribute('disabled'), false))
})

it('virtualizes long lists and updates the visible window on scroll', () => {
  const original = dom.HTMLElement.prototype.getBoundingClientRect
  dom.HTMLElement.prototype.getBoundingClientRect = () => new dom.DOMRect(0, 0, 500, 100)
  try {
    const view = render(<VirtualList items={Array.from({ length: 1000 }, (_, index) => index)} itemKey={item => item} estimate={100} renderItem={item => <span data-testid="row">{item}</span>} />)
    assert.ok(view.getAllByTestId('row').length < 20)
    const viewport = view.container.firstElementChild!
    Object.defineProperty(viewport, 'clientHeight', { value: 500, configurable: true })
    fireEvent.scroll(viewport, { target: { scrollTop: 5000 } })
    assert.ok(view.getAllByTestId('row').length < 20)
    assert.ok(view.getByText('50'))
    assert.equal(view.queryByText('0'), null)
  } finally { dom.HTMLElement.prototype.getBoundingClientRect = original }
})

it('uses the supplied images for conversation avatars and the welcome logo', () => {
  const props = { loading: false, streaming: false, page: 1, totalPages: 1, initialEnd: false, onPageChange: async () => {} }
  const view = render(<MessageList {...props} turns={[]} />)
  const welcomeSource = view.getByAltText('Kaguya').getAttribute('src')
  assert.ok(welcomeSource?.endsWith('/images/kaguya.png'))
  view.rerender(<MessageList {...props} turns={[{ turn_index: 1, user_content: '你好', model_name: '', model_id: '', api_protocol: '', started_at: new Date().toISOString(), duration_ms: 0, tool_calls: 0, blocks: [] }]} />)
  assert.equal(view.getByAltText('Kaguya 头像').getAttribute('src'), welcomeSource)
  assert.ok(view.getByAltText('用户头像').getAttribute('src')?.endsWith('/assets/lyonmu.png'))
})

it('shows one dash per message page and synchronizes the selected page', () => {
  let selected = 0
  const props = { turns: [], loading: false, streaming: false, totalPages: 3, initialEnd: false, onPageChange: async (page: number) => { selected = page } }
  const view = render(<MessageList {...props} page={3} />)
  assert.equal(view.getByLabelText('第 3 页').getAttribute('aria-current'), 'page')
  fireEvent.click(view.getByLabelText('第 1 页'))
  assert.equal(selected, 1)
  view.rerender(<MessageList {...props} page={1} />)
  assert.equal(view.getByLabelText('第 1 页').getAttribute('aria-current'), 'page')
  assert.equal(view.getByLabelText('第 3 页').getAttribute('aria-current'), null)
  assert.equal(view.container.querySelectorAll('.chat-page-rail button').length, 3)
})

it('opens AI providers when entering system settings', () => {
  let page = ''
  const view = render(<AppLayout colorMode="light" currentPage="system-info" onPageChange={value => { page = value }} onToggleColorMode={() => {}} />)
  fireEvent.click(view.getByLabelText('系统管理'))
  assert.equal(page, 'ai-providers')
})
