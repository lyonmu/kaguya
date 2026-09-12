/// <reference types="node" />
import { afterEach, describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { get, guardFailure, guardSuccess, isApiEnvelope, type PayloadGuard } from './http'

const originalFetch = globalThis.fetch
afterEach(() => { globalThis.fetch = originalFetch })

const guard: PayloadGuard<{ id: string }> = value =>
  typeof value === 'object' && value !== null && typeof (value as { id?: unknown }).id === 'string'
    ? guardSuccess({ id: (value as { id: string }).id })
    : guardFailure('数据格式不正确')

describe('api response validation', () => {
  it('accepts valid envelopes and unknown extra fields', () => {
    assert.equal(isApiEnvelope({ code: 100000, data: { id: 'a' }, extra: true }), true)
    assert.equal(isApiEnvelope({ code: '100000' }), false)
    assert.equal(isApiEnvelope(null), false)
    assert.equal(isApiEnvelope([{ code: 1 }]), false)
  })

  it('rejects bad payloads instead of casting them', async () => {
    globalThis.fetch = (async () => Response.json({ code: 100000, data: null })) as typeof fetch
    await assert.rejects(get('/x', undefined, undefined, guard), /数据格式不正确/)

    globalThis.fetch = (async () => Response.json({ code: 100000, data: [1, 2] })) as typeof fetch
    await assert.rejects(get('/x', undefined, undefined, guard), /数据格式不正确/)

    globalThis.fetch = (async () => Response.json({ code: 100000, data: { id: 'ok', future: 1 } })) as typeof fetch
    assert.deepEqual(await get('/x', undefined, undefined, guard), { id: 'ok' })
  })

  it('surfaces business errors and preserves cancellation', async () => {
    globalThis.fetch = (async () => Response.json({ code: 102010, message: '密钥不可用' })) as typeof fetch
    await assert.rejects(get('/x', undefined, undefined, guard), /密钥不可用/)

    const controller = new AbortController()
    globalThis.fetch = (async () => { controller.abort(); throw new DOMException('Aborted', 'AbortError') }) as typeof fetch
    await assert.rejects(get('/x', undefined, controller.signal, guard), (error: unknown) => error instanceof DOMException)
  })
})
