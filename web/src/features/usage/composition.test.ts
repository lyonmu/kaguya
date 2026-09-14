import { it } from 'node:test'
import assert from 'node:assert/strict'
import { compositionLabelWidth } from './composition'

it('fits composition label width into the per-category band', () => {
  // 尚未测量或没有类目时使用默认宽度。
  assert.equal(compositionLabelWidth(0, 10), 84)
  assert.equal(compositionLabelWidth(1005, 0), 84)
  // (宽度 - 85) / 类目数 - 8 向下取档位，保证标签不超出每个类目的可用宽度。
  assert.equal(compositionLabelWidth(1005, 10), 84)
  assert.equal(compositionLabelWidth(663, 10), 48)
  assert.equal(compositionLabelWidth(1312, 10), 112)
  // 类目很少时标签宽度封顶。
  assert.equal(compositionLabelWidth(1005, 1), 136)
})
