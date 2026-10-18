import { it } from 'node:test'
import assert from 'node:assert/strict'
import { turnGuard } from './guards'

const turn = { turn_index: 1, user_content: 'task', started_at: '2026-10-06T00:00:00Z', blocks: [] }
const audit = { version: 1, summary_calls: 2, reported_calls: 1, unreported_calls: 1,
  usage: { input_tokens: 10, output_tokens: 20, total_tokens: 42, cache_hit_tokens: 5, cache_creation_tokens: 7, reasoning_tokens: 3 } }
it('accepts legacy turns and separately reported preparation usage', () => {
  assert.equal(turnGuard(turn).ok, true)
  const checked = turnGuard({ ...turn, preparation_usage: audit })
  assert.equal(checked.ok, true)
  if (checked.ok) assert.equal(checked.value.preparation_usage?.unreported_calls, 1)
})
it('rejects malformed preparation counts, unknown versions and token values', () => {
  for (const changed of [{ ...audit, version: 2 }, { ...audit, summary_calls: 3 }, { ...audit, reported_calls: -1 }, { ...audit, summary_calls: NaN }, { ...audit, unreported_calls: 0.5 }, { ...audit, usage: { ...audit.usage, cache_creation_tokens: '7' } }, { ...audit, usage: { ...audit.usage, total_tokens: Infinity } }]) {
    assert.equal(turnGuard({ ...turn, preparation_usage: changed }).ok, false)
  }
})
