import { it } from 'node:test'
import assert from 'node:assert/strict'
import { aggregateActivity } from './activity'

it('aggregates UTC weeks across years and preserves zero activity', () => {
  const days = [
    { date: '2024-12-30', total_tokens: 10, conversations: 1 },
    { date: '2025-01-01', total_tokens: 20, conversations: 1 },
    { date: '2025-01-05', total_tokens: 30, conversations: 1 },
    { date: '2025-01-06', total_tokens: 0, conversations: 0 },
  ]
  assert.deepEqual(aggregateActivity(days, 'weekly'), [['2024-12-30', 60], ['2025-01-06', 0]])
  assert.deepEqual(aggregateActivity(days, 'monthly'), [['2024-12', 10], ['2025-01', 50]])
  assert.deepEqual(aggregateActivity(days, 'daily'), days.map(day => [day.date, day.total_tokens]))
  assert.deepEqual(aggregateActivity([], 'daily'), [])
})
