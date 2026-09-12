import { it } from 'node:test'
import assert from 'node:assert/strict'
import { activityPieces, aggregateActivity } from './activity'

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

it('splits heatmap levels by quantile so small usage stays visible', () => {
  const colors = ['c0', 'c1', 'c2', 'c3']
  const values = [0, 0, 10, 12, 14, 16, 1000000]
  const pieces = activityPieces(values, colors, String)
  // 空白日期固定透明，非零日期使用不透明颜色。
  assert.deepEqual(pieces[0], { value: 0, color: 'transparent', label: '0' })
  assert.deepEqual(pieces.map(piece => piece.color), ['transparent', 'c0', 'c1', 'c2', 'c3'])
  assert.deepEqual(pieces.slice(1), [
    { gte: 1, lte: 11, color: 'c0', label: '1 - 11' },
    { gte: 12, lte: 13, color: 'c1', label: '12 - 13' },
    { gte: 14, lte: 15, color: 'c2', label: '14 - 15' },
    { gte: 16, color: 'c3', label: '≥ 16' },
  ])
  // 每个非零取值恰好落在一档内：极值不会把其他日期压进与空白日无法区分的最低档。
  for (const value of values.filter(value => value > 0)) {
    assert.equal(pieces.filter(piece => piece.gte !== undefined && piece.gte <= value && (piece.lte ?? value) >= value).length, 1)
  }
})

it('keeps a single level when every day has the same usage or none', () => {
  assert.deepEqual(activityPieces([0, 0], ['c0', 'c1'], String), [{ value: 0, color: 'transparent', label: '0' }])
  assert.deepEqual(activityPieces([5, 5, 5], ['c0', 'c1'], String), [
    { value: 0, color: 'transparent', label: '0' },
    { gte: 1, lte: 4, color: 'c0', label: '1 - 4' },
    { gte: 5, color: 'c1', label: '≥ 5' },
  ])
})
