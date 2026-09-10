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
const { TLSConfigPanel } = await import('./TLSConfigPanel')
const { ModelCascader } = await import('../../features/providers/ModelCascader')
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
  const config = { context_compaction_percent: 90, agent_max_steps: 64, command_timeout_seconds: 120, global_agents_paths: ['~/.config/agents/AGENTS.md', '~/.codex/AGENTS.md'], system_prompt: '', user_agent: 'kaguya', default_model_id: '', task_model_id: '', global_system_prompt: '只读基础人设' }
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
  fireEvent.change(view.getByLabelText('会话压缩比例'), { target: { value: '75' } })
  fireEvent.change(view.getByLabelText('User-Agent'), { target: { value: 'Configured/2' } })
  fireEvent.change(view.getByLabelText('自定义系统提示词'), { target: { value: '请简洁回答' } })
  assert.equal(view.queryByText('模型选择统一在这里管理'), null)
  assert.equal(view.queryByText('用于服务端的聊天和标题生成请求，不修改浏览器请求头。'), null)
  for (const [label, provider, model] of [['默认对话模型', '提供商 A', '聊天模型'], ['后台任务模型', '提供商 B', '任务模型']]) {
    const selector = view.getByRole('combobox', { name: label })
    fireEvent.mouseDown(selector.closest('.ant-select')!.querySelector('.ant-select-selector') ?? selector)
    const popup = await waitFor(() => {
      const element = Array.from(dom.document.querySelectorAll('.ant-cascader-dropdown:not(.ant-select-dropdown-hidden)')).at(-1)
      assert.ok(element)
      return element as unknown as HTMLElement
    })
    fireEvent.click(within(popup).getByText(provider))
    fireEvent.click(await within(popup).findByText(model))
  }
  fireEvent.click(view.getByRole('button', { name: /保存配置/ }))
  await waitFor(() => assert.deepEqual(saved, { context_compaction_percent: 75, agent_max_steps: 64, command_timeout_seconds: 120, global_agents_paths: config.global_agents_paths, system_prompt: '请简洁回答', user_agent: 'Configured/2', default_model_id: 'local-chat', task_model_id: 'local-task' }))
  assert.ok(view.getByText('只读基础人设'))
})

it('searches models by API ID, clears configuration and restores the chat default', async () => {
  const models = [
    { label: '同名模型', value: 'local-a', provider_name: '提供商 A', provider_id: 'a', model_id: 'api-alpha' },
    { label: '同名模型', value: 'local-b', provider_name: '提供商 B', provider_id: 'b', model_id: 'api-beta' },
  ]
  let selected = ''
  const view = render(<ModelCascader aria-label="模型" models={models} onChange={value => { selected = value }} />)
  const input = view.getByRole('combobox', { name: '模型' })
  fireEvent.change(input, { target: { value: 'API-BETA' } })
  const result = await view.findByText('提供商 B / 同名模型')
  fireEvent.click(result)
  assert.equal(selected, 'local-b')
  view.rerender(<ModelCascader aria-label="模型" models={models} value={selected} onChange={value => { selected = value }} />)
  const clear = view.container.querySelector('.ant-select-clear')
  assert.ok(clear)
  fireEvent.click(clear)
  assert.equal(selected, '')
  view.unmount()
  selected = 'local-a'
  const chat = render(<ModelCascader aria-label="模型" models={models} value={selected} defaultOption onChange={value => { selected = value }} />)
  const chatInput = chat.getByRole('combobox', { name: '模型' })
  fireEvent.mouseDown(chatInput.closest('.ant-select')!.querySelector('.ant-select-selector') ?? chatInput)
  fireEvent.click(await chat.findByText('默认模型'))
  assert.equal(selected, '')
})

it('saves TLS imports without retaining private keys and reports restart requirements', async () => {
  let saved: Record<string, unknown> | undefined
  const tls = { certificate_pem: 'public-certificate', fingerprint: 'abc123', not_after: '2027-09-10T00:00:00Z', hosts: ['localhost'] }
  globalThis.fetch = (async (url, init) => {
    assert.ok(String(url).endsWith('/info/tls'))
    saved = JSON.parse(String(init?.body))
    return response(tls)
  }) as typeof fetch
  let updated: unknown
  const view = render(<App><TLSConfigPanel info={tls} onSaved={value => { updated = value }} /></App>)
  assert.ok(view.getByRole('link', { name: '下载公钥证书' }))
  fireEvent.click(view.getByText('导入已有证书与私钥'))
  fireEvent.change(view.getByLabelText('TLS 证书 PEM'), { target: { value: 'replacement-cert' } })
  fireEvent.change(view.getByLabelText('TLS 私钥 PEM'), { target: { value: 'secret-key' } })
  fireEvent.click(view.getByRole('button', { name: '保存导入证书' }))
  await waitFor(() => assert.deepEqual(saved, { certificate_pem: 'replacement-cert', private_key_pem: 'secret-key' }))
  await waitFor(() => assert.equal((view.getByLabelText('TLS 私钥 PEM') as HTMLTextAreaElement).value, ''))
  assert.deepEqual(updated, tls)
  assert.ok(view.getByText('证书已更新，请重启服务并重新信任新证书'))
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
