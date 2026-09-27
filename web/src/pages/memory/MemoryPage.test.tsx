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

// waitForText 在 happy-dom 下轮询异步挂载的抽屉/弹层内容；测试环境的
// waitFor 有时看不到 portal 中刚提交的节点。
async function waitForText(view: ReturnType<typeof render>, matcher: string | RegExp) {
  for (let i = 0; i < 100; i++) {
    if (view.queryByText(matcher)) return
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 20)) })
  }
  throw new Error(`text not found: ${String(matcher)}`)
}
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
  // Match the legacy Go response that caused Desktop to render a blank page.
  aliases: ['记忆存储'], related_ids: null, source_count: 2,
  claims: [{
    key: 'storage', statement: 'Memory 继续使用 SQLCipher', basis: 'user_statement',
    evidence: [{ source_id: 's-1', part_key: 'user', quote: '继续用 SQLCipher', relation: 'support', source: 'conversation c-1 / turn t-7 / user' }],
  }],
  created_at: '2026-09-24T00:00:00Z', updated_at: '2026-09-24T00:00:00Z',
}
const job = {
  id: 'j-1', kind: 'compile', scope_key: 'personal', status: 'needs_review', attempt: 1,
  created_at: '2026-09-24T00:00:00Z', input_tokens: 10, output_tokens: 5, total_tokens: 15, calls: 2,
  proposal: { changes: [{ title: '待确认决定', summary: '需要核验', body: '待审正文', reason: '有新证据', claims: [] }] },
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
  fireEvent.click(view.getByRole('button', { name: /Expand row|展开行/ }))
  await waitFor(() => assert.notEqual(view.queryByText('待审正文'), null))
  fireEvent.click(view.getByRole('button', { name: /批\s*准/ }))
  await waitFor(() => assert.equal(approved.endsWith('/v1/memory/jobs/j-1/approve'), true))
  // 状态与独立用量标签。
  fireEvent.click(view.getByRole('tab', { name: '状态与用量' }))
  await waitFor(() => assert.notEqual(view.queryByText(/独立于聊天口径/), null))
  assert.notEqual(view.queryByText('含未知用量，不按 0 计'), null)
})

it('renders legacy empty details in Desktop and opens links through the host', async () => {
  const previousLocation = Object.getOwnPropertyDescriptor(globalThis, 'location')
  Object.defineProperty(globalThis, 'location', { configurable: true, value: { protocol: 'wails:', hostname: 'localhost', hash: '#api-prefix=/kaguya/api' } })
  let opened = ''
  globalThis.fetch = (async (url, init) => {
    const target = String(url)
    if (target === '/__desktop/open-external') {
      opened = JSON.parse(String(init?.body)).url
      return new Response(null, { status: 204 })
    }
    if (target.includes('/pages/p-1')) return response({ ...detail, body: '[资料链接](https://example.com/source)\n\n![参考图](https://example.com/image.png)' })
    if (target.includes('/pages')) return response({ total: 1, page: 1, page_size: 20, items: [pageItem] })
    if (target.includes('/jobs') || target.includes('/sources') || target.includes('/project')) return response({ total: 0, page: 1, page_size: 20, items: [] })
    return response(status)
  }) as typeof fetch
  try {
    const view = render(<App><MemoryPage /></App>)
    await waitFor(() => assert.ok(view.queryByText('Memory 使用 SQLCipher')))
    fireEvent.click(view.getAllByText('Memory 使用 SQLCipher')[0]!)
    await waitFor(() => assert.ok(view.queryByRole('link', { name: '资料链接' })))
    assert.equal(view.queryByRole('img', { name: '参考图' }), null)
    fireEvent.click(view.getByRole('link', { name: '资料链接' }))
    await waitFor(() => assert.equal(opened, 'https://example.com/source'))
    assert.ok(view.getByText('依据'))
    cleanup()
  } finally {
    if (previousLocation) Object.defineProperty(globalThis, 'location', previousLocation)
    else Reflect.deleteProperty(globalThis, 'location')
  }
})

it('requests subsequent memory pages instead of hiding records after twenty', async () => {
  let requestedPage = ''
  globalThis.fetch = (async url => {
    const target = String(url)
    if (target.includes('/pages')) {
      requestedPage = new URL(target, 'http://localhost').searchParams.get('page') ?? ''
      return response({ total: 21, page: Number(requestedPage), page_size: 20, items: [{ ...pageItem, id: requestedPage === '2' ? 'p-21' : 'p-1', title: requestedPage === '2' ? '第二页记忆' : pageItem.title }] })
    }
    if (target.includes('/jobs') || target.includes('/sources') || target.includes('/project')) return response({ total: 0, page: 1, page_size: 20, items: [] })
    return response(status)
  }) as typeof fetch
  const view = render(<App><MemoryPage /></App>)
  await waitFor(() => assert.ok(view.queryByText(pageItem.title)))
  fireEvent.click(view.getByTitle('2'))
  await waitFor(() => assert.ok(view.queryByText('第二页记忆')))
  assert.equal(requestedPage, '2')
})

it('navigates from evidence to source', async () => {
  const source = {
    id: 's-1', kind: 'turn', scope_key: 'personal', state: 'processed', source_key: 'turn:t-7:projection-v1',
    conversation_id: 'c-1', turn_id: 't-7', turn_status: 'completed', policy_epoch: 0,
    captured_at: '2026-09-24T00:00:00Z', available: true,
    parts: [{ part_key: 'user', origin: 'user_statement', text: '继续用 SQLCipher', truncated: false }],
  }
  globalThis.fetch = (async url => {
    const target = String(url)
    if (target.includes('/sources/s-1')) return response(source)
    if (target.includes('/pages/p-1/revisions')) return response([])
    if (target.includes('/pages/p-1')) return response(detail)
    if (target.includes('/pages')) return response({ total: 1, page: 1, page_size: 20, items: [pageItem] })
    if (target.includes('/jobs')) return response({ total: 0, page: 1, page_size: 20, items: [] })
    if (target.includes('/project')) return response({ total: 0, page: 1, page_size: 20, items: [] })
    return response(status)
  }) as typeof fetch
  const view = render(<App><MemoryPage /></App>)
  await waitFor(() => assert.notEqual(view.queryByText('Memory 使用 SQLCipher'), null))
  fireEvent.click(view.getAllByText('Memory 使用 SQLCipher')[0]!)
  await waitFor(() => assert.notEqual(view.queryByText('继续使用 SQLCipher。'), null))
  // 来源导航：从证据跳到原始轮次片段。
  fireEvent.click(view.getByRole('button', { name: '查看来源' }))
  await waitFor(() => assert.notEqual(view.queryByText('继续用 SQLCipher'), null))
})

it('compares a historical revision with the current version', async () => {
  const diff = {
    page_id: 'p-1',
    from: { version: 1, actor: 'user', reason: '', created_at: '2026-09-24T00:00:00Z', title: '旧标题', summary: '', body: '旧正文', kind: 'decision', status: 'active', aliases: [] },
    to: { version: 2, actor: 'task_model', reason: '整合', created_at: '2026-09-24T01:00:00Z', title: '新标题', summary: '', body: '新正文', kind: 'decision', status: 'active', aliases: [] },
    content_changes: [{ field: 'body', from: '旧正文', to: '新正文' }],
    metadata_changes: [{ field: 'aliases', from: '', to: '检索' }],
    claim_changes: [{ key: 'storage', change: 'added', to: '新主张' }],
    evidence_changes: [],
  }
  const revisions = [{
    version: 1, actor: 'user', reason: '', title: '旧标题', summary: '', body: '旧正文',
    status: 'active', claim_keys: [], created_at: '2026-09-24T00:00:00Z',
  }]
  globalThis.fetch = (async url => {
    const target = String(url)
    if (target.includes('/diff')) return response(diff)
    if (target.includes('/pages/p-1/revisions')) return response(revisions)
    if (target.includes('/pages/p-1')) return response(detail)
    if (target.includes('/pages')) return response({ total: 1, page: 1, page_size: 20, items: [pageItem] })
    if (target.includes('/jobs')) return response({ total: 0, page: 1, page_size: 20, items: [] })
    if (target.includes('/project')) return response({ total: 0, page: 1, page_size: 20, items: [] })
    return response(status)
  }) as typeof fetch
  const view = render(<App><MemoryPage /></App>)
  await waitFor(() => assert.notEqual(view.queryByText('Memory 使用 SQLCipher'), null))
  fireEvent.click(view.getAllByText('Memory 使用 SQLCipher')[0]!)
  await waitFor(() => assert.notEqual(view.queryByText('继续使用 SQLCipher。'), null))
  fireEvent.click(view.getByRole('button', { name: /版\s*本/ }))
  await waitForText(view, '旧标题')
  fireEvent.click(view.getByRole('button', { name: '对比当前' }))
  await waitForText(view, /新正文/)
  assert.notEqual(view.queryByText('版本对比'), null)
})
