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
const previous = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)] as const))
for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })

const { App } = await import('antd')
const { cleanup, fireEvent, render, waitFor } = await import('@testing-library/react')
const { ModelCatalogPage } = await import('./ModelCatalogPage')

afterEach(async () => {
  cleanup()
  await new Promise(resolve => setTimeout(resolve, 0))
})
after(() => {
  for (const [key, descriptor] of previous) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor)
    else delete (globalThis as Record<string, unknown>)[key]
  }
})

it('shows the latest-first local catalog and searches through the API', async () => {
  const requests: URL[] = []
  globalThis.fetch = (async input => {
    const url = new URL(String(input), 'http://localhost')
    requests.push(url)
    if (url.pathname.endsWith('/system/info')) return Response.json({ code: 100000, data: { model_sync_url: 'https://mirror.example/models.json', model_sync_catalog_count: 2, model_sync_last_success_at: '2026-09-14T01:00:00Z', model_sync_last_error: '' } })
    const keyword = url.searchParams.get('keyword') ?? ''
    const items = keyword ? [{ id: 'openai/gpt-new', provider_id: 'openai', provider_name: 'OpenAI', model_id: 'gpt-new', name: 'GPT New', family: 'gpt', description: 'Newest GPT', reasoning_enabled: 1, token_context_window: 128000, token_max_output_tokens: 32000, capability_tool_use: 1, capability_vision: 1, capability_structured_output: 1, input_modalities: ['text', 'image'], release_date: '2026-09-12', last_updated: '2026-09-13' }] : [
      { id: 'openai/gpt-new', provider_id: 'openai', provider_name: 'OpenAI', model_id: 'gpt-new', name: 'GPT New', family: 'gpt', description: 'Newest GPT', reasoning_enabled: 1, token_context_window: 128000, token_max_output_tokens: 32000, capability_tool_use: 1, capability_vision: 1, capability_structured_output: 1, input_modalities: ['text', 'image'], release_date: '2026-09-12', last_updated: '2026-09-13' },
      { id: 'anthropic/claude-old', provider_id: 'anthropic', provider_name: 'Anthropic', model_id: 'claude-old', name: 'Claude Old', family: 'claude', description: '', reasoning_enabled: 0, token_context_window: 200000, token_max_output_tokens: 8000, capability_tool_use: 1, capability_vision: 0, capability_structured_output: 0, input_modalities: ['text'], release_date: '2026-08-01', last_updated: '2026-08-02' },
    ]
    return Response.json({ code: 100000, data: { total: items.length, items, page: 1, page_size: 20 } })
  }) as typeof fetch

  const view = render(<App><ModelCatalogPage /></App>)
  await waitFor(() => assert.ok(view.getByText('GPT New')))
  const rows = view.container.querySelectorAll('tbody tr[data-row-key]')
  assert.match(rows[0]?.textContent ?? '', /GPT New/)
  assert.ok(view.getByText('OpenAI'))
  assert.match(view.getByText(/来源：/).textContent ?? '', /mirror\.example/)
  fireEvent.click(view.getByRole('button', { name: '查看 GPT New 详情' }))
  const drawer = await waitFor(() => view.getByRole('dialog'))
  assert.match(drawer.textContent ?? '', /模型详情/)
  assert.match(drawer.textContent ?? '', /Newest GPT/)
  assert.match(drawer.textContent ?? '', /128,000/)
  assert.match(drawer.textContent ?? '', /结构化输出支持/)
  fireEvent.click(view.getByRole('button', { name: 'Close' }))
  fireEvent.change(view.getByRole('searchbox', { name: '检索模型目录' }), { target: { value: 'gpt' } })
  fireEvent.keyDown(view.getByRole('searchbox', { name: '检索模型目录' }), { key: 'Enter', code: 'Enter' })
  await waitFor(() => assert.ok(requests.some(url => url.pathname.endsWith('/model/catalog') && url.searchParams.get('keyword') === 'gpt')))
  assert.equal(view.queryByText('Claude Old'), null)
})
