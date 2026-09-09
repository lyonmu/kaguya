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
const { render, fireEvent, cleanup, waitFor, act, within } = await import('@testing-library/react')
const { App } = await import('antd')
const { SystemInfoPage } = await import('./SystemInfoPage')
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

it('loads, edits and saves system config with local model record IDs', async () => {
  let saved: Record<string, unknown> | undefined
  const config = { system_prompt: '', user_agent: 'kaguya', default_model_id: '', task_model_id: '', global_system_prompt: '只读基础人设' }
  globalThis.fetch = (async (url, init) => {
    if (String(url).includes('/model/label')) return response([
      { label: '聊天模型', value: 'local-chat', provider_name: '提供商 A', provider_id: 'p', model_id: 'api-chat' },
      { label: '任务模型', value: 'local-task', provider_name: '提供商 B', provider_id: 'p2', model_id: 'api-task' },
    ])
    if (init?.method === 'PUT') { saved = JSON.parse(String(init.body)); return response({ ...config, ...saved }) }
    return response(config)
  }) as typeof fetch
  const view = render(<App><SystemInfoPage /></App>)
  await waitFor(() => assert.ok(view.getByText('只读基础人设')))
  fireEvent.change(view.getByLabelText('User-Agent'), { target: { value: 'Configured/2' } })
  fireEvent.change(view.getByLabelText('自定义系统提示词'), { target: { value: '请简洁回答' } })
  for (const [label, option] of [['默认对话模型', '提供商 A / 聊天模型'], ['后台任务模型', '提供商 B / 任务模型']]) {
    const selector = view.getByRole('combobox', { name: label })
    fireEvent.mouseDown(selector.closest('.ant-select')!.querySelector('.ant-select-selector') ?? selector)
    const popup = await waitFor(() => {
      const list = dom.document.getElementById(selector.getAttribute('aria-controls')!)
      const element = list?.closest('.ant-select-dropdown')
      assert.ok(element)
      return element as unknown as HTMLElement
    })
    fireEvent.click(within(popup).getByText(option))
  }
  fireEvent.click(view.getByRole('button', { name: /保存配置/ }))
  await waitFor(() => assert.deepEqual(saved, { system_prompt: '请简洁回答', user_agent: 'Configured/2', default_model_id: 'local-chat', task_model_id: 'local-task' }))
  assert.ok(view.getByText('只读基础人设'))
})

it('shows a config loading error instead of an editable empty form', async () => {
  globalThis.fetch = (async () => Response.json({ code: 999999, message: '加载失败' })) as typeof fetch
  const view = render(<App><SystemInfoPage /></App>)
  await waitFor(() => assert.ok(view.getByText('加载失败')))
  assert.equal(view.queryByRole('button', { name: /保存配置/ }), null)
})

it('toggles the system sidebar and exposes system config in mobile navigation', async () => {
  let next = ''
  const view = render(<AppLayout colorMode="light" currentPage="ai-providers" onPageChange={value => { next = value }} onToggleColorMode={() => {}} />)
  fireEvent.click(view.getByLabelText('收起系统菜单'))
  assert.equal(view.getByLabelText('展开系统菜单').getAttribute('aria-expanded'), 'false')
  fireEvent.click(view.getByLabelText('展开系统菜单'))
  assert.equal(view.getByLabelText('收起系统菜单').getAttribute('aria-expanded'), 'true')
  fireEvent.click(view.getByLabelText('打开系统菜单'))
  const dialog = await waitFor(() => view.getByRole('dialog'))
  const configItem = Array.from(dialog.querySelectorAll('[role="menuitem"]')).find(item => item.textContent?.includes('系统配置'))
  assert.ok(configItem)
  fireEvent.click(configItem)
  assert.equal(next, 'system-info')
  await waitFor(() => assert.equal(view.getByLabelText('打开系统菜单').getAttribute('aria-expanded'), 'false'))
})
