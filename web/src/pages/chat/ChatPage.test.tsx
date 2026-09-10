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
const { render, fireEvent, cleanup, waitFor, act, within } = await import('@testing-library/react')
const { App } = await import('antd')
const { ChatPage: WorkspaceChatPage } = await import('./ChatPage')
const { ChatProvider } = await import('../../features/chat/ChatProvider')
function ChatPage() { return <ChatProvider><WorkspaceChatPage /></ChatProvider> }
const { AppLayout } = await import('../../components/layout/AppLayout')
const { VirtualList } = await import('../../features/chat/components/VirtualList')
const { MessageList: RuntimeMessageList } = await import('../../features/chat/components/MessageList')
const { AssistantThread } = await import('../../features/chat/components/AssistantThread')
function MessageList(props: React.ComponentProps<typeof RuntimeMessageList>) {
  return <AssistantThread turns={props.turns} streaming={props.streaming} disabled={false} onSend={async () => {}} onStop={() => {}}><RuntimeMessageList {...props} /></AssistantThread>
}
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
  const popup = await waitFor(() => {
    const element = Array.from(dom.document.querySelectorAll('.ant-cascader-dropdown:not(.ant-select-dropdown-hidden)')).at(-1)
    assert.ok(element)
    return element as unknown as HTMLElement
  })
  fireEvent.click(await within(popup).findByText('提供商 A'))
  fireEvent.click(await within(popup).findByText('模型 A'))
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

it('reveals a selected conversation outside the virtual window and supports repeated jumps', () => {
  const original = dom.HTMLElement.prototype.getBoundingClientRect
  dom.HTMLElement.prototype.getBoundingClientRect = () => new dom.DOMRect(0, 0, 500, 100)
  try {
    const props = { items: Array.from({ length: 1000 }, (_, index) => index), itemKey: (item: number) => item, estimate: 100, renderItem: (item: number) => <span data-testid="row">{item}</span> }
    const view = render(<VirtualList {...props} />)
    const viewport = view.container.firstElementChild!
    Object.defineProperty(viewport, 'clientHeight', { value: 500, configurable: true })
    view.rerender(<VirtualList {...props} reveal={{ key: 800, request: {} }} />)
    assert.ok(view.getByText('800'))
    assert.ok(view.getAllByTestId('row').length < 20)
    fireEvent.scroll(viewport, { target: { scrollTop: 0 } })
    assert.equal(view.queryByText('800'), null)
    view.rerender(<VirtualList {...props} reveal={{ key: 800, request: {} }} />)
    assert.ok(view.getByText('800'))
  } finally { dom.HTMLElement.prototype.getBoundingClientRect = original }
})

it('links recent running sessions to their project or ordinary list without cancelling streams', async () => {
  const project = { id: 'p1', name: '项目一', path: '/root/one', description: '', created_at: '' }
  let hideProject = false
  let delayProject = false
  let resolveProject: ((value: Response) => void) | undefined
  const streams: Array<{ signal: AbortSignal; start: () => void }> = []
  globalThis.fetch = (async (url, init) => {
    const parsed = new URL(String(url), 'http://localhost')
    const path = parsed.pathname
    if (path.endsWith('/sse')) {
      const index = streams.length + 1
      const signal = init!.signal as AbortSignal
      return new Response(new ReadableStream({ start(controller) {
        streams.push({ signal, start() {
          controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ code: 100000, data: { chat: { id: `c${index}`, flag: 'start' } } })}\n\n`))
        } })
        signal.addEventListener('abort', () => controller.error(new DOMException('Aborted', 'AbortError')))
      } }), { headers: { 'Content-Type': 'text/event-stream' } })
    }
    if (path.endsWith('/project/page')) return response({ items: hideProject ? [] : [project], total: hideProject ? 0 : 1 })
    if (path.endsWith('/project/p1')) {
      if (delayProject) return new Promise<Response>(resolve => { resolveProject = resolve })
      return response(project)
    }
    if (path.includes('/model/label')) return response([])
    return response({ items: [], total: 0 })
  }) as typeof fetch
  const view = render(<App><ChatPage /></App>)
  fireEvent.click(view.getByRole('radio', { name: '项目' }))
  fireEvent.click(await view.findByRole('button', { name: 'folder-open 项目一' }))
  fireEvent.change(view.getByLabelText('对话消息'), { target: { value: '项目任务' } })
  fireEvent.click(view.getByLabelText('发送消息'))
  await waitFor(() => assert.equal(streams.length, 1))
  fireEvent.click(view.getByRole('radio', { name: '对话' }))
  fireEvent.change(view.getByLabelText('对话消息'), { target: { value: '普通任务' } })
  fireEvent.click(view.getByLabelText('发送消息'))
  await waitFor(() => assert.equal(streams.length, 2))
  hideProject = true
  fireEvent.click(view.getByRole('button', { name: '◌ 项目任务 正在运行 · 项目' }))
  await waitFor(() => assert.ok(view.container.querySelector('.project-group-name.selected')))
  assert.equal((view.getByRole('radio', { name: '项目' }) as HTMLInputElement).checked, true)
  assert.equal(view.container.querySelector('.project-conversation.active')?.textContent, '项目任务')
  await act(async () => { streams[0].start(); streams[1].start() })
  assert.equal(view.container.querySelector('.project-conversation.active')?.textContent, '项目任务')
  fireEvent.click(view.getByRole('button', { name: '◌ 普通任务 正在运行' }))
  assert.equal((view.getByRole('radio', { name: '对话' }) as HTMLInputElement).checked, true)
  assert.equal(view.container.querySelector('.chat-session-list .active .chat-session-title')?.textContent, '普通任务')
  fireEvent.change(view.getByLabelText('搜索对话标题前缀'), { target: { value: '无匹配项' } })
  fireEvent.click(view.getByRole('button', { name: '◌ 普通任务 正在运行' }))
  assert.equal((view.getByLabelText('搜索对话标题前缀') as HTMLInputElement).value, '')
  delayProject = true
  fireEvent.click(view.getByRole('button', { name: '◌ 项目任务 正在运行 · 项目' }))
  await waitFor(() => assert.ok(resolveProject))
  fireEvent.click(view.getByRole('button', { name: '◌ 普通任务 正在运行' }))
  await act(async () => { resolveProject!(response(project)) })
  assert.equal((view.getByRole('radio', { name: '对话' }) as HTMLInputElement).checked, true)
  assert.equal(view.container.querySelector('.project-group'), null)
  assert.ok(streams.every(stream => !stream.signal.aborted))
})

it('uses the supplied images for conversation avatars and the welcome logo', () => {
  const props = { loading: false, streaming: false, page: 1, totalPages: 1, initialEnd: false, onPageChange: async () => {} }
  const view = render(<MessageList {...props} turns={[]} />)
  const welcomeSource = view.getByAltText('Kaguya').getAttribute('src')
  assert.ok(welcomeSource?.endsWith('/assets/kaguya.png'))
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

it('runs two conversations through assistant-ui and retains them across system navigation', async () => {
  const streams: Array<{ signal: AbortSignal; emit: (flag: string, text?: string) => void }> = []
  globalThis.fetch = (async (url, init) => {
    const path = String(url)
    if (path.endsWith('/sse')) {
      const index = streams.length + 1
      const signal = init!.signal as AbortSignal
      return new Response(new ReadableStream({ start(controller) {
        streams.push({ signal, emit(flag, text) {
          controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ code: 100000, data: { chat: { id: `conv-${index}`, flag, content: text }, usage: { total_tokens: 1 } } })}\n\n`))
          if (flag === 'done') controller.close()
        } })
        signal.addEventListener('abort', () => controller.error(new DOMException('Aborted', 'AbortError')))
      } }), { headers: { 'Content-Type': 'text/event-stream' } })
    }
    if (path.includes('/model/label')) return response([])
    if (path.includes('/page')) return response({ items: [], total: 0 })
    if (path.endsWith('/context')) return response({ percent: null })
    return response({ id: path.split('/').at(-1), title: '已完成的测试', turn_count: 1 })
  }) as typeof fetch
  const { useState } = await import('react')
  function Navigation() {
    const [page, setPage] = useState<import('../../components/layout/AppLayout').SystemPage>('chat')
    return <App><ChatProvider><AppLayout colorMode="light" currentPage={page} onPageChange={setPage} onToggleColorMode={() => {}}>{page === 'chat' ? <WorkspaceChatPage /> : <div>配置页面</div>}</AppLayout></ChatProvider></App>
  }
  const view = render(<Navigation />)
  fireEvent.change(view.getByLabelText('对话消息'), { target: { value: 'first task' } })
  fireEvent.click(view.getByLabelText('发送消息'))
  await waitFor(() => assert.equal(streams.length, 1))
  fireEvent.click(view.getByRole('button', { name: 'plus 新建对话' }))
  fireEvent.change(view.getByLabelText('对话消息'), { target: { value: 'second task' } })
  fireEvent.keyDown(view.getByLabelText('对话消息'), { key: 'Enter' })
  await waitFor(() => assert.equal(streams.length, 2))
  await act(async () => { streams[0].emit('start'); streams[1].emit('start'); streams[0].emit('delta', 'first answer'); streams[1].emit('delta', 'second answer') })
  assert.ok(view.getByText('second answer'))
  assert.equal(view.queryByText('first answer'), null)
  const footer = within(view.container.querySelector('.chat-sidebar-footer')! as HTMLElement)
  fireEvent.click(footer.getByLabelText('系统管理'))
  assert.ok(view.getByText('配置页面'))
  assert.ok(streams.every(stream => !stream.signal.aborted))
  await act(async () => { streams[0].emit('done') })
  fireEvent.click(view.getByLabelText('对话管理'))
  assert.ok(view.getByText('second answer'))
  assert.equal(streams[1].signal.aborted, false)
  fireEvent.click(view.getByRole('button', { name: '已完成的测试 查看结果' }))
  assert.ok(view.getByText('first answer'))
  fireEvent.click(view.getByRole('button', { name: '◌ second task 正在运行' }))
  fireEvent.click(view.getByRole('button', { name: 'stop 停止' }))
  await waitFor(() => assert.equal(streams[1].signal.aborted, true))
  assert.equal(streams[0].signal.aborted, false)
})
