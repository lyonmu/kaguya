import { it } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { developmentCA } from './dev-ca'

it('loads the default public certificate and honors explicit CA paths without insecure fallback', () => {
  const home = mkdtempSync(join(tmpdir(), 'kaguya-ca-'))
  try {
    mkdirSync(join(home, '.kaguya'))
    writeFileSync(join(home, '.kaguya/kaguya.crt'), 'default public certificate')
    assert.equal(developmentCA('', home).toString(), 'default public certificate')
    const custom = join(home, 'custom.crt')
    writeFileSync(custom, 'replacement public certificate')
    assert.equal(developmentCA(custom, home).toString(), 'replacement public certificate')
    assert.throws(() => developmentCA(join(home, 'missing.crt'), home), /KAGUYA_CA_CERT/)
    assert.throws(() => developmentCA('', join(home, 'missing-home')), /prepare-tls/)
  } finally { rmSync(home, { recursive: true, force: true }) }
})
