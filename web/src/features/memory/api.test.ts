import { afterEach, it } from 'node:test'
import assert from 'node:assert/strict'
import { approveMemoryJob, createMemoryPage, fetchMemoryPages, updateMemoryPage } from './api'

const originalFetch = globalThis.fetch
afterEach(() => { globalThis.fetch = originalFetch })

const pageItem = {
  id: 'p-1', scope_key: 'personal', kind: 'decision', canonical_key: 'storage', title: 'Memory 使用 SQLCipher',
  summary: '不新增第二个数据库', status: 'active', version: 1, pinned: false, user_locked: false, updated_at: '2026-09-24T00:00:00Z',
}

it('validates memory list payloads and rejects malformed data', async () => {
  globalThis.fetch = (async () => Response.json({ code: 100000, data: { total: 1, page: 1, page_size: 20, items: [pageItem] } })) as typeof fetch
  const list = await fetchMemoryPages({ scope_key: 'personal' })
  assert.equal(list.total, 1)
  assert.equal(list.items[0]?.title, 'Memory 使用 SQLCipher')

  globalThis.fetch = (async () => Response.json({ code: 100000, data: { total: 'bad', items: [] } })) as typeof fetch
  await assert.rejects(() => fetchMemoryPages({}), /记忆列表响应格式异常/)
})

it('sends create and patch payloads with expected_version', async () => {
  const requests: Array<{ url: string; method?: string; body?: unknown }> = []
  const detail = {
    id: 'p-1', version: 1, scope_key: 'personal', kind: 'fact', title: '标题', summary: '', body: '正文',
    status: 'active', pinned: false, user_locked: false, aliases: [], claims: [], related_ids: [], source_count: 1,
    created_at: '2026-09-24T00:00:00Z', updated_at: '2026-09-24T00:00:00Z',
  }
  globalThis.fetch = (async (url, init) => {
    requests.push({ url: String(url), method: init?.method, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    return Response.json({ code: 100000, data: detail })
  }) as typeof fetch
  await createMemoryPage({ scope_key: 'personal', kind: 'fact', title: '标题', body: '正文' })
  await updateMemoryPage('p-1', { expected_version: 1, pinned: true })
  assert.equal(requests[0]?.method, 'POST')
  assert.equal(requests[1]?.method, 'PATCH')
  assert.deepEqual(requests[1]?.body, { expected_version: 1, pinned: true })
  await approveMemoryJob('j-1')
  assert.equal(requests[2]?.url.endsWith('/v1/memory/jobs/j-1/approve'), true)
})
