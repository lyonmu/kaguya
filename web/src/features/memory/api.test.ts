import { afterEach, it } from 'node:test'
import assert from 'node:assert/strict'
import {
  approveMemoryJob, createMemoryPage, fetchMemoryPage, fetchMemoryPageDiff, fetchMemoryPages, fetchMemorySource,
  fetchMemorySources, importMemoryDocument, startMemoryBackfill, updateMemoryPage,
} from './api'

const originalFetch = globalThis.fetch
afterEach(() => { globalThis.fetch = originalFetch })

const pageItem = {
  id: 'p-1', scope_key: 'personal', kind: 'decision', canonical_key: 'storage', title: 'Memory 使用 SQLCipher',
  summary: '不新增第二个数据库', status: 'active', version: 1, pinned: false, user_locked: false, updated_at: '2026-09-24T00:00:00Z',
}

it('normalizes empty Go slices and rejects malformed detail collections', async () => {
  const detail = { ...pageItem, body: '正文', source_count: 0, aliases: null, claims: [{ key: 'k', statement: '内容', basis: 'synthesis', evidence: null }], related_ids: null }
  globalThis.fetch = (async () => Response.json({ code: 100000, data: detail })) as typeof fetch
  const result = await fetchMemoryPage('p-1')
  assert.deepEqual(result.related_ids, [])
  assert.deepEqual(result.aliases, [])
  assert.deepEqual(result.claims[0]?.evidence, [])
  for (const related_ids of ['bad', [42], [{}]]) {
    globalThis.fetch = (async () => Response.json({ code: 100000, data: { ...detail, related_ids } })) as typeof fetch
    await assert.rejects(() => fetchMemoryPage('p-1'), /记忆详情响应格式异常/)
  }
})

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

it('guards backfill, import, source and diff payloads', async () => {
  const requests: Array<{ url: string; body?: unknown }> = []
  const responses: Record<string, unknown> = {
    '/v1/memory/backfill': { job_id: 'j-1', scope_key: 'personal', status: 'pending', scanned: 0, created: 0, skipped: 0, max_sources: 5, limited: false, finished: false },
    '/v1/memory/import': { source_id: 's-1', state: 'pending', path: 'docs/spec.md', size: 10, deduplicated: false },
    '/v1/memory/sources': { total: 1, page: 1, page_size: 20, items: [{ id: 's-1', kind: 'turn', scope_key: 'personal', state: 'pending', source_key: 'turn:t:projection-v1', policy_epoch: 0, captured_at: '2026-09-24T00:00:00Z' }] },
    '/v1/memory/sources/s-1': { id: 's-1', kind: 'turn', scope_key: 'personal', state: 'pending', source_key: 'turn:t:projection-v1', policy_epoch: 0, captured_at: '2026-09-24T00:00:00Z', available: true, parts: [{ part_key: 'user', origin: 'user_statement', text: '内容', truncated: false }] },
    '/v1/memory/pages/p-1/diff': { page_id: 'p-1', from: { version: 1 }, to: { version: 2 }, content_changes: [], metadata_changes: [], claim_changes: [], evidence_changes: [] },
  }
  globalThis.fetch = (async (url, init) => {
    const target = String(url).split('?')[0] ?? ''
    requests.push({ url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined })
    const key = Object.keys(responses).find(candidate => target.endsWith(candidate))
    return Response.json({ code: 100000, data: key ? responses[key] : null })
  }) as typeof fetch
  const backfill = await startMemoryBackfill({ scope_key: 'personal', max_sources: 5 })
  assert.equal(backfill.job_id, 'j-1')
  const imported = await importMemoryDocument({ scope_key: 'project:p-1', path: 'docs/spec.md' })
  assert.equal(imported.deduplicated, false)
  const sources = await fetchMemorySources({ kind: 'turn' })
  assert.equal(sources.items[0]?.id, 's-1')
  const source = await fetchMemorySource('s-1')
  assert.equal(source.available, true)
  assert.equal(source.parts[0]?.part_key, 'user')
  const diff = await fetchMemoryPageDiff('p-1', 1, 2)
  assert.equal(diff.from.version, 1)
  assert.equal(requests[0]?.body && (requests[0].body as Record<string, unknown>).max_sources, 5)

  globalThis.fetch = (async () => Response.json({ code: 100000, data: { job_id: 1 } })) as typeof fetch
  await assert.rejects(() => startMemoryBackfill({ scope_key: 'personal' }), /历史回填响应格式异常/)
})
