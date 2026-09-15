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

it('browses the catalog without add actions', async () => {
  globalThis.fetch = (async (url: RequestInfo | URL) => {
    const target = new URL(String(url), 'http://localhost')
    if (target.pathname.endsWith('/provider/catalog')) {
      return Response.json({ code: 100000, data: { total: 2, items: [openai, anthropic], page: 1, page_size: 20 } })
    }
    return Response.json({ code: 100000, data: {} })
  }) as typeof fetch

  const view = render(<App><ProviderCatalogPage /></App>)
  await waitFor(() => assert.ok(view.getByText('Anthropic')))
  // 添加入口统一在「提供商与模型」页，这里不再提供添加按钮。
  assert.equal(view.queryByRole('button', { name: /添\s*加/ }), null)
  assert.ok(view.getByText('@ai-sdk/anthropic'))
  assert.ok(view.getByText('https://api.anthropic.com/v1'))
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
