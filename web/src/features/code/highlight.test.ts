import { it } from 'node:test'
import assert from 'node:assert/strict'
import { HIGHLIGHT_MAX_CHARS, escapeHtml, highlightCode, langFromPath, splitHighlightedLines } from './highlight'

it('splits highlighted html into balanced single-line fragments', () => {
  const lines = splitHighlightedLines('<span class="hljs-comment">a\nb</span>\n<span class="hljs-string">c</span>')
  assert.deepEqual(lines, [
    '<span class="hljs-comment">a</span>',
    '<span class="hljs-comment">b</span>',
    '<span class="hljs-string">c</span>',
  ])
  for (const line of lines) {
    assert.equal((line.match(/<span/g) ?? []).length, (line.match(/<\/span>/g) ?? []).length)
  }
})

it('keeps a line per source line and closes trailing spans', () => {
  const lines = splitHighlightedLines('first\n<span class="hljs-title">second\nthird</span>')
  assert.deepEqual(lines, ['first', '<span class="hljs-title">second</span>', '<span class="hljs-title">third</span>'])
  assert.equal(splitHighlightedLines('a\nb\n').length, 3)
})

it('escapes html and maps common languages from file names', () => {
  assert.equal(escapeHtml('<a & b>'), '&lt;a &amp; b&gt;')
  assert.equal(langFromPath('src/main.go'), 'go')
  assert.equal(langFromPath('web/src/App.tsx'), 'typescript')
  assert.equal(langFromPath('ops/Dockerfile'), 'dockerfile')
  assert.equal(langFromPath('a/b/Makefile'), 'makefile')
  assert.equal(langFromPath('conf/app.yaml'), 'yaml')
  assert.equal(langFromPath('conf/app.toml'), 'ini')
  assert.equal(langFromPath('.bashrc'), 'bash')
  assert.equal(langFromPath('LICENSE'), '')
  assert.equal(langFromPath('data.unknown'), '')
})

it('only highlights known languages within the size limit', () => {
  const html = highlightCode('const value = 1', 'typescript')
  assert.ok(html?.includes('hljs-keyword'))
  assert.equal(highlightCode('plain', ''), undefined)
  assert.equal(highlightCode('plain', 'not-a-language'), undefined)
  assert.equal(highlightCode('x'.repeat(HIGHLIGHT_MAX_CHARS + 1), 'typescript'), undefined)
})
