import { it } from 'node:test'
import assert from 'node:assert/strict'
import { activeMention } from './mentions'
it('finds an unquoted active file mention and ignores email addresses', () => {
  assert.equal(activeMention('a@example.com', 13), undefined)
  assert.deepEqual(activeMention('读取 @src/ma', 10), { start: 3, end: 10, query: 'src/ma' })
  assert.deepEqual(activeMention('@', 1), { start: 0, end: 1, query: '' })
  assert.equal(activeMention('读取 @docs/my file', 16), undefined)
})
