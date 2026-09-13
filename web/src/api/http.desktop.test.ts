/// <reference types="node" />
import { afterEach, describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { buildUrl } from './http'

const originalFetch = globalThis.fetch
const originalLocation = Object.getOwnPropertyDescriptor(globalThis, 'location')
afterEach(() => {
  globalThis.fetch = originalFetch
  if (originalLocation) Object.defineProperty(globalThis, 'location', originalLocation)
  else Reflect.deleteProperty(globalThis, 'location')
})

function useDesktopLocation(hash = '#api-prefix=%2Fkaguya%2Fapi') {
  Object.defineProperty(globalThis, 'location', {
    configurable: true,
    writable: true,
    value: { protocol: 'wails:', hostname: 'localhost', hash },
  })
}

describe('desktop api base', () => {
  it('uses the native relative prefix for normal requests and SSE', async () => {
    useDesktopLocation()
    assert.equal(buildUrl('/v1/system/info'), '/kaguya/api/v1/system/info')
    assert.equal(buildUrl('/v1/chat/conversation/page', { page: 1 }), '/kaguya/api/v1/chat/conversation/page?page=1')

    const { streamChat } = await import('../features/chat/api')
    let requested = ''
    globalThis.fetch = (async (url, init) => {
      requested = String(url)
      assert.equal(init?.method, 'POST')
      return new Response('data: {"code":100000,"data":{"chat":{"id":"1","flag":"done"}}}\n\n', {
        headers: { 'Content-Type': 'text/event-stream' },
      })
    }) as typeof fetch
    await streamChat('1', '你好', new AbortController().signal, () => {})
    assert.equal(requested, '/kaguya/api/v1/chat/sse')
  })

  it('ignores an external build-time base in desktop mode', () => {
    useDesktopLocation('#api-prefix=%2Fkaguya%2Fapi')
    // VITE_API_BASE_URL 只影响 Web 分支；这里断言前缀仍来自宿主注入。
    assert.equal(buildUrl('/v1/system/info'), '/kaguya/api/v1/system/info')
  })

  it('rejects requests with a broken host prefix instead of using an external base', () => {
    useDesktopLocation('#api-prefix=%2Fwails')
    assert.throws(() => buildUrl('/v1/system/info'), /保留路径冲突/)
  })

  it('keeps the web base outside desktop', () => {
    Reflect.deleteProperty(globalThis, 'location')
    assert.equal(buildUrl('/v1/system/info'), '/kaguya/api/v1/system/info')
  })
})
