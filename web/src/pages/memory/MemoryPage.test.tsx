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
const { MemoryPage } = await import('./MemoryPage')
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

const pageItem = {
  id: 'p-1', scope_key: 'personal', kind: 'decision', canonical_key: 'storage', title: 'Memory 使用 SQLCipher',
  summary: '不新增第二个数据库', status: 'active', version: 1, pinned: false, user_locked: false, updated_at: '2026-09-24T00:00:00Z',
}
const detail = {
  id: 'p-1', version: 1, scope_key: 'personal', kind: 'decision', title: 'Memory 使用 SQLCipher',
  summary: '不新增第二个数据库', body: '## 决定\n继续使用 SQLCipher。', status: 'active', pinned: false, user_locked: false,
  aliases: ['记忆存储'], related_ids: [], source_count: 2,
  claims: [{
    key: 'storage', statement: 'Memory 继续使用 SQLCipher', basis: 'user_statement',
    evidence: [{ source_id: 's-1', part_key: 'user', quote: '继续用 SQLCipher', relation: 'support', source: 'conversation c-1 / turn t-7 / user' }],
  }],
  created_at: '2026-09-24T00:00:00Z', updated_at: '2026-09-24T00:00:00Z',
}
const job = {
  id: 'j-1', kind: 'compile', scope_key: 'personal', status: 'needs_review', attempt: 1,
  created_at: '2026-09-24T00:00:00Z', input_tokens: 10, output_tokens: 5, total_tokens: 15, calls: 2,
}
const status = {
  enabled: true, auto_capture: true, context_tokens: 2000, policy_epoch: 3, pending_sources: 2,
  active_pages: 4, index_normalizer: 1, task_model_set: true, blocked_jobs: 0, failed_jobs: 0, review_jobs: 1,
  memory_input_tokens: 10, memory_output_tokens: 5, memory_total_tokens: 15, memory_calls: 2, memory_usage_known: false,
}

it('renders pages with evidence and resolves review jobs', async () => {
  let approved = ''
  globalThis.fetch = (async (url, init) => {
    const target = String(url)
    if (init?.method === 'POST' && target.endsWith('/approve')) { approved = target; return response(null) }
    if (target.includes('/pages/p-1/revisions')) return response([])
    if (target.includes('/pages/p-1')) return response(detail)
    if (target.includes('/pages')) return response({ total: 1, page: 1, page_size: 20, items: [pageItem] })
    if (target.includes('/jobs')) return response({ total: 1, page: 1, page_size: 20, items: [job] })
    return response(status)
  }) as typeof fetch
  const view = render(<App><MemoryPage /></App>)
  await waitFor(() => assert.notEqual(view.queryByText('Memory 使用 SQLCipher'), null))
  fireEvent.click(view.getAllByText('Memory 使用 SQLCipher')[0]!)
  await waitFor(() => assert.notEqual(view.queryByText('继续使用 SQLCipher。'), null))
  // 依据标签与来源展示。
  assert.notEqual(view.queryByText('用户明确决定'), null)
  assert.notEqual(view.queryByText(/conversation c-1/), null)
  // 待审区：批准走任务接口。
  fireEvent.click(view.getByRole('tab', { name: '任务与待审' }))
  await waitFor(() => assert.notEqual(view.queryByText('needs_review'), null))
  fireEvent.click(view.getByRole('button', { name: /批\s*准/ }))
  await waitFor(() => assert.equal(approved.endsWith('/v1/memory/jobs/j-1/approve'), true))
  // 状态与独立用量标签。
  fireEvent.click(view.getByRole('tab', { name: '状态与用量' }))
  await waitFor(() => assert.notEqual(view.queryByText(/独立于聊天口径/), null))
  assert.notEqual(view.queryByText('含未知用量，不按 0 计'), null)
})
