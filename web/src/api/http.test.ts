/// <reference types="node" />
import { afterEach, describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { ApiRequestError } from './errors'
import { del, get, guardFailure, guardSuccess, isApiEnvelope, patch, put, type PayloadGuard } from './http'

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

describe('api error classification', () => {
  it('classifies business, http and protocol failures without faking a code', async () => {
    globalThis.fetch = (async () => Response.json({ code: 102003, message: '会话不存在' })) as typeof fetch
    await assert.rejects(get('/x'), (error: unknown) =>
      error instanceof ApiRequestError && error.kind === 'business' && error.code === 102003)

    globalThis.fetch = (async () => new Response(JSON.stringify({ code: 100002, message: '请求被拒绝' }), { status: 403 })) as typeof fetch
    await assert.rejects(get('/x'), (error: unknown) =>
      error instanceof ApiRequestError && error.kind === 'business' && error.status === 403 && error.code === 100002)

    // 代理返回的上游非 JSON 响应：保留 status，归类 http，不伪造业务码。
    globalThis.fetch = (async () => new Response('<html>bad gateway</html>', { status: 502 })) as typeof fetch
    await assert.rejects(get('/x'), (error: unknown) =>
      error instanceof ApiRequestError && error.kind === 'http' && error.status === 502 && error.code === undefined)

    globalThis.fetch = (async () => new Response('not-json', { status: 200 })) as typeof fetch
    await assert.rejects(get('/x'), (error: unknown) =>
      error instanceof ApiRequestError && error.kind === 'protocol')

    globalThis.fetch = (async () => { throw new TypeError('Failed to fetch') }) as typeof fetch
    await assert.rejects(get('/x'), (error: unknown) =>
      error instanceof ApiRequestError && error.kind === 'network')
  })

  it('passes an AbortSignal through every method', async () => {
    const signals: Array<AbortSignal | null | undefined> = []
    globalThis.fetch = (async (_url: unknown, init?: RequestInit) => {
      signals.push(init?.signal)
      return Response.json({ code: 100000, data: {} })
    }) as typeof fetch
    const controller = new AbortController()
    await put('/x', { a: 1 }, controller.signal)
    await patch('/x', { a: 1 }, controller.signal)
    await del('/x', undefined, controller.signal)
    assert.equal(signals.length, 3)
    for (const signal of signals) assert.equal(signal, controller.signal)
  })
})
