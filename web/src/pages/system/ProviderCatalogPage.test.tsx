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
const { act, cleanup, fireEvent, render, waitFor } = await import('@testing-library/react')
const { ProviderCatalogPage } = await import('./ProviderCatalogPage')

const originalFetch = globalThis.fetch
afterEach(async () => {
  cleanup()
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
  globalThis.fetch = originalFetch
})
after(() => {
  for (const [key, descriptor] of previous) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor)
    else delete (globalThis as Record<string, unknown>)[key]
  }
})

const openai = { id: 'openai', name: 'OpenAI', api: 'https://api.openai.com/v1', npm: '@ai-sdk/openai', doc: 'https://platform.openai.com/docs', model_count: 120 }
const anthropic = { id: 'anthropic', name: 'Anthropic', api: 'https://api.anthropic.com/v1', npm: '@ai-sdk/anthropic', doc: '', model_count: 30 }

it('adds a provider from the catalog with prefilled name and root URL', async () => {
  const bodies: Record<string, unknown>[] = []
  globalThis.fetch = (async (url: RequestInfo | URL, init?: RequestInit) => {
    const target = new URL(String(url), 'http://localhost')
    if (target.pathname.endsWith('/provider/catalog')) {
      const keyword = target.searchParams.get('keyword') ?? ''
      const items = keyword ? [openai] : [openai, anthropic]
      return Response.json({ code: 100000, data: { total: items.length, items, page: 1, page_size: 20 } })
    }
    if (target.pathname.endsWith('/provider/label')) return Response.json({ code: 100000, data: [{ label: 'OpenAI', value: 'p1' }] })
    if (target.pathname.endsWith('/provider') && init?.method === 'POST') {
      bodies.push(JSON.parse(String(init.body)))
      return Response.json({ code: 100000, data: {} })
    }
    return Response.json({ code: 100000, data: {} })
  }) as typeof fetch

  const view = render(<App><ProviderCatalogPage /></App>)
  await waitFor(() => assert.ok(view.getByText('Anthropic')))
  // 已添加的提供商只展示标记，不再提供添加按钮。
  assert.ok(view.getByText('已添加'))

  fireEvent.click(view.getByRole('button', { name: /添\s*加/ }))
  await waitFor(() => assert.ok(view.baseElement.querySelector<HTMLInputElement>('input#provider_name')))
  assert.equal(view.baseElement.querySelector<HTMLInputElement>('input#provider_name')?.value, 'Anthropic')
  assert.equal(view.baseElement.querySelector<HTMLInputElement>('input#base_url')?.value, 'https://api.anthropic.com/v1')

  await act(async () => {
    fireEvent.click(view.baseElement.querySelector<HTMLButtonElement>('.ant-modal-footer .ant-btn-primary')!)
  })
  await waitFor(() => assert.equal(bodies.length, 1))
  assert.deepEqual(bodies[0], {
    provider_type: 'normal', provider_name: 'Anthropic', base_url: 'https://api.anthropic.com/v1', api_key: '',
  })
})

it('searches the catalog through the API and reports the synced counts', async () => {
  const requests: URL[] = []
  globalThis.fetch = (async (url: RequestInfo | URL, init?: RequestInit) => {
    const target = new URL(String(url), 'http://localhost')
    requests.push(target)
    if (target.pathname.endsWith('/provider/catalog')) {
      const keyword = target.searchParams.get('keyword') ?? ''
      const items = keyword ? [anthropic] : [openai, anthropic]
      return Response.json({ code: 100000, data: { total: items.length, items, page: 1, page_size: 20 } })
    }
    if (target.pathname.endsWith('/provider/label')) return Response.json({ code: 100000, data: [] })
    if (target.pathname.endsWith('/model/sync') && init?.method === 'POST') {
      return Response.json({ code: 100000, data: { count: 3, provider_count: 2, synced_at: '2026-09-14T01:00:00Z' } })
    }
    return Response.json({ code: 100000, data: {} })
  }) as typeof fetch

  const view = render(<App><ProviderCatalogPage /></App>)
  await waitFor(() => assert.ok(view.getByText('OpenAI')))

  fireEvent.change(view.getByRole('searchbox', { name: '检索提供商目录' }), { target: { value: 'anthropic' } })
  fireEvent.keyDown(view.getByRole('searchbox', { name: '检索提供商目录' }), { key: 'Enter', code: 'Enter' })
  await waitFor(() => assert.ok(requests.some(url => url.pathname.endsWith('/provider/catalog') && url.searchParams.get('keyword') === 'anthropic')))
  assert.equal(view.queryByText('OpenAI'), null)

  await act(async () => {
    fireEvent.click(view.getByRole('button', { name: /立即同步/ }))
  })
  await waitFor(() => assert.ok(requests.some(url => url.pathname.endsWith('/model/sync'))))
  await waitFor(() => assert.ok(view.getByText(/已同步 2 个提供商/)))
})
