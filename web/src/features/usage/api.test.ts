import { afterEach, it } from 'node:test'
import assert from 'node:assert/strict'
import { fetchTokenUsage } from './api'
import { usageDateRange } from './range'
import { fetchAccessLogs } from '../access-logs/api'

const originalFetch = globalThis.fetch
afterEach(() => { globalThis.fetch = originalFetch })

it('sends Unix seconds using the same parameter names for usage and access logs', async () => {
  const requests: URL[] = []
  globalThis.fetch = (async url => {
    requests.push(new URL(String(url), 'http://localhost'))
    return Response.json({ code: 100000, data: { items: [], total: 0 } })
  }) as typeof fetch
  const [startTime, endTime] = usageDateRange('2025-01-01', '2025-01-03')
  await fetchTokenUsage(startTime, endTime)
  await fetchAccessLogs({ startTime, endTime, page: 1, pageSize: 10 })
  for (const url of requests) {
    assert.equal(url.searchParams.get('start_time'), '1735689600')
    assert.equal(url.searchParams.get('end_time'), '1735948799')
    assert.equal(url.searchParams.has('start'), false)
    assert.equal(url.searchParams.has('end'), false)
  }
  await fetchTokenUsage()
  assert.equal(requests.at(-1)?.search, '')
})
