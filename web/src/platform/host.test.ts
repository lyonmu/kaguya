/// <reference types="node" />
import { afterEach, describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { copyText, desktopApiBase, isDesktopLocation, parseDesktopApiPrefix } from './host'

const originalFetch = globalThis.fetch
const originalLocation = Object.getOwnPropertyDescriptor(globalThis, 'location')
const originalNavigator = Object.getOwnPropertyDescriptor(globalThis, 'navigator')
const originalDocument = Object.getOwnPropertyDescriptor(globalThis, 'document')
afterEach(() => {
  globalThis.fetch = originalFetch
  for (const [key, descriptor] of [['location', originalLocation], ['navigator', originalNavigator], ['document', originalDocument]] as const) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor)
    else Reflect.deleteProperty(globalThis, key)
  }
})

/** useInsecureContext 模拟以 IP 打开的 Web 模式：没有异步剪贴板，只有选区复制。 */
function useInsecureContext(execCommand: (command: string) => boolean) {
  Reflect.deleteProperty(globalThis, 'location')
  const area = {
    value: '',
    setAttribute() { /* 隐藏 textarea 不可编辑 */ },
    style: {} as Record<string, string>,
    select() { area.selected = area.value },
    remove() { area.removed = true },
    selected: '',
    removed: false,
  }
  const commands: string[] = []
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: {} })
  Object.defineProperty(globalThis, 'document', {
    configurable: true,
    value: {
      body: { appendChild() { /* 不校验节点插入 */ } },
      createElement: () => area,
      getSelection: () => ({ rangeCount: 0, removeAllRanges() { /* 无原选区 */ }, addRange() { /* 无原选区 */ } }),
      execCommand: (command: string) => { commands.push(command); return execCommand(command) },
    },
  })
  return { area, commands }
}

function useDesktopLocation(hash = '#api-prefix=%2Fkaguya%2Fapi') {
  Object.defineProperty(globalThis, 'location', {
    configurable: true,
    writable: true,
    value: { protocol: 'wails:', hostname: 'localhost', hash },
  })
}

describe('desktop host detection', () => {
  it('only treats the native scheme as desktop', () => {
    assert.equal(isDesktopLocation({ protocol: 'wails:', hostname: 'localhost', hash: '' }), true)
    assert.equal(isDesktopLocation({ protocol: 'http:', hostname: 'localhost', hash: '' }), false)
    assert.equal(isDesktopLocation({ protocol: 'https:', hostname: '127.0.0.1', hash: '' }), false)
    assert.equal(isDesktopLocation({ protocol: 'wails:', hostname: 'example.com', hash: '' }), false)
  })

  it('parses the injected prefix and rejects illegal values', () => {
    assert.deepEqual(parseDesktopApiPrefix('#api-prefix=%2Fkaguya%2Fapi'), { ok: true, prefix: '/kaguya/api' })
    assert.deepEqual(parseDesktopApiPrefix('#api-prefix=%2Fapi'), { ok: true, prefix: '/api' })
    for (const hash of [
      '',
      '#other=1',
      '#api-prefix=',
      '#api-prefix=kaguya%2Fapi',
      '#api-prefix=%2F',
      '#api-prefix=%2Fkaguya%2F',
      '#api-prefix=%2Fa%2F%2Fb',
      '#api-prefix=%2Fa%2F..%2Fb',
      '#api-prefix=%2Fa%3Fx%3D1',
      '#api-prefix=%2Fwails',
      '#api-prefix=%2F__desktop',
      '#api-prefix=%2Fassets',
      '#api-prefix=%2Fkaguya-favicon.webp',
    ]) {
      const result = parseDesktopApiPrefix(hash)
      assert.equal(result.ok, false, `accepted ${hash}`)
    }
  })

  it('never falls back to an external base when the prefix is invalid', () => {
    useDesktopLocation('#api-prefix=')
    const resolved = desktopApiBase()
    assert.equal(resolved.ok, false)
  })

  it('posts native system operations instead of using browser APIs', async () => {
    useDesktopLocation()
    const calls: Array<{ url: string; init?: RequestInit }> = []
    globalThis.fetch = (async (url, init) => {
      calls.push({ url: String(url), init: init as RequestInit })
      return Response.json({ ok: true })
    }) as typeof fetch

    await copyText('你好')
    assert.equal(calls[0].url, '/__desktop/clipboard')
    assert.equal(calls[0].init?.method, 'POST')
    assert.deepEqual(JSON.parse(String(calls[0].init?.body)), { text: '你好' })
    const headers = (calls[0].init?.headers ?? {}) as Record<string, string>
    assert.equal(headers['Content-Type'], 'application/json')

    globalThis.fetch = (async (url, init) => {
      calls.push({ url: String(url), init: init as RequestInit })
      return Response.json({ ok: true })
    }) as typeof fetch
    const { openExternal } = await import('./host')
    await openExternal('https://example.com/docs')
    assert.equal(calls[1].url, '/__desktop/open-external')
    assert.deepEqual(JSON.parse(String(calls[1].init?.body)), { url: 'https://example.com/docs' })
  })

  it('surfaces native failures instead of reporting success', async () => {
    useDesktopLocation()
    globalThis.fetch = (async () => new Response(JSON.stringify({ error: 'clipboard write failed' }), { status: 500 })) as typeof fetch
    await assert.rejects(copyText('x'), /系统操作失败/)
  })

  it('uses the browser clipboard outside desktop', async () => {
    Reflect.deleteProperty(globalThis, 'location')
    let copied = ''
    Object.defineProperty(globalThis, 'navigator', {
      configurable: true,
      value: { clipboard: { writeText: async (text: string) => { copied = text } } },
    })
    await copyText('web')
    assert.equal(copied, 'web')
  })

  it('copies with the selection fallback when the page is not a secure context', async () => {
    const { area, commands } = useInsecureContext(() => true)
    await copyText('以 IP 打开的页面')
    assert.equal(area.selected, '以 IP 打开的页面')
    assert.deepEqual(commands, ['copy'])
    assert.equal(area.removed, true)
  })

  it('reports a failure when the selection fallback cannot copy', async () => {
    const { area } = useInsecureContext(() => false)
    await assert.rejects(copyText('x'), /无法写入剪贴板/)
    assert.equal(area.removed, true)
  })
})
