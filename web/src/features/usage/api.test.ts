import { afterEach, it } from 'node:test'
import assert from 'node:assert/strict'
import { fetchTokenUsage } from './api'
import { usageDateRange } from './range'

const originalFetch = globalThis.fetch
afterEach(() => { globalThis.fetch = originalFetch })

it('sends Unix seconds using the expected parameter names', async () => {
  const requests: URL[] = []
  globalThis.fetch = (async url => {
    requests.push(new URL(String(url), 'http://localhost'))
    return Response.json({ code: 100000, data: { items: [], total: 0 } })
  }) as typeof fetch
  const [startTime, endTime] = usageDateRange('2025-01-01', '2025-01-03')
  await fetchTokenUsage(startTime, endTime)
  for (const url of requests) {
    assert.equal(url.searchParams.get('start_time'), '1735689600')
    assert.equal(url.searchParams.get('end_time'), '1735948799')
    assert.equal(url.searchParams.has('start'), false)
    assert.equal(url.searchParams.has('end'), false)
  }
  await fetchTokenUsage()
  assert.equal(requests.at(-1)?.search, '')
})
