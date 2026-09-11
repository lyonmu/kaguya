import { it } from 'node:test'
import assert from 'node:assert/strict'
import { fileIconForPath, iconNameForPath } from './fileIcons'

it('resolves material icon names by file name and extension', () => {
  assert.equal(iconNameForPath('main.go'), 'go')
  assert.equal(iconNameForPath('web/src/App.tsx'), 'react_ts')
  assert.equal(iconNameForPath('web/src/App.test.tsx'), 'react_ts')
  assert.equal(iconNameForPath('go.mod'), 'go-mod')
  assert.equal(iconNameForPath('.gitignore'), 'git')
  assert.equal(iconNameForPath('Dockerfile'), 'docker')
  assert.equal(iconNameForPath('README.md'), 'readme')
  assert.equal(iconNameForPath('LICENSE'), 'license')
  assert.equal(iconNameForPath('data.unknown'), 'file')
})

it('falls back to the generic file icon with embedded svg data', () => {
  const icon = fileIconForPath('data.unknown')
  assert.equal(icon.width, 16)
  assert.ok(icon.body.startsWith('<path'))
  assert.ok(fileIconForPath('main.go').body.length > 0)
})
