import { it } from 'node:test'
import assert from 'node:assert/strict'
import { activeMention, mentionToken, referencedFiles } from './mentions'
it('round-trips file names with spaces and quotes, deduplicates, and ignores email', () => {
  const path = 'docs/a "quoted" file.md'
  assert.deepEqual(referencedFiles(`Read ${mentionToken(path)} ${mentionToken(path)} @src/main.go a@example.com`), [path, 'src/main.go'])
  assert.equal(activeMention('a@example.com', 13), undefined)
  assert.deepEqual(activeMention('读取 @src/ma', 10), { start: 3, end: 10, query: 'src/ma' })
  assert.deepEqual(referencedFiles('@"unfinished'), [])
})
