/// <reference types="node" />
import { after, afterEach, it } from 'node:test'
import assert from 'node:assert/strict'
import { Window } from 'happy-dom'
import type { MCPServer } from '../../features/mcp/types'

const dom = new Window({ url: 'http://localhost' })
const globals = {
  window: dom, document: dom.document, navigator: dom.navigator,
  HTMLElement: dom.HTMLElement, Element: dom.Element, Node: dom.Node,
  SVGElement: dom.SVGElement, ShadowRoot: dom.ShadowRoot,
  MutationObserver: dom.MutationObserver, ResizeObserver: dom.ResizeObserver,
  getComputedStyle: dom.getComputedStyle.bind(dom), IS_REACT_ACT_ENVIRONMENT: true,
}
const previous = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
const { render, fireEvent, cleanup, waitFor, act, within } = await import('@testing-library/react')
const { App } = await import('antd')
const { MCPManagementPanel } = await import('./MCPManagementPanel')
const originalFetch = globalThis.fetch
const response = (data: unknown) => Response.json({ code: 100000, data })
afterEach(async () => {
  cleanup()
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
  globalThis.fetch = originalFetch
})
after(async () => {
  await dom.happyDOM.close()
  for (const [key, descriptor] of previous) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor)
    else Reflect.deleteProperty(globalThis, key)
  }
})

const server: MCPServer = { id: 'mcp-1', name: '示例服务', transport: 'streamable-http', command: '', args: [], env: {}, working_directory: '', url: 'https://example.com/mcp', headers: {}, timeout_seconds: 60, enabled: false, status: { state: 'stopped', tools: [] }, created_at: '2026-09-10T00:00:00Z', updated_at: '2026-09-10T00:00:00Z' }

it('serializes visibility polling, pauses hidden timers and cleans up on unmount', async () => {
  const nativeSetTimeout = globalThis.setTimeout
  const nativeClearTimeout = globalThis.clearTimeout
  const visibility = Object.getOwnPropertyDescriptor(document, 'visibilityState')
  let now = 0
  const timers = new Map<ReturnType<typeof setTimeout>, { at: number; run: () => void }>()
  // Only the panel's five-second polling timers use this clock; Ant animations keep real timers.
  globalThis.setTimeout = ((fn: () => void, delay?: number, ...args: unknown[]) => {
    if (delay !== 5000) return nativeSetTimeout(fn, delay, ...args)
    const id = {} as ReturnType<typeof setTimeout>
    timers.set(id, { at: now + delay, run: fn })
    return id
  }) as typeof setTimeout
  globalThis.clearTimeout = ((id: ReturnType<typeof setTimeout>) => { if (!timers.delete(id)) nativeClearTimeout(id) }) as typeof clearTimeout
  const changeVisibility = (value: 'hidden' | 'visible') => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, value })
    document.dispatchEvent(new dom.Event('visibilitychange') as unknown as Event)
  }
  const pending: Array<{ resolve: (value: Response) => void; signal?: AbortSignal | null }> = []
  globalThis.fetch = (async (_url, init) => new Promise<Response>(resolve => { pending.push({ resolve, signal: init?.signal }) })) as typeof fetch
  const finish = async (index: number) => act(async () => { pending[index].resolve(response({ items: [server], total: 1, page: 1, page_size: 10 })); await Promise.resolve() })
  const advance = async (ms: number) => act(async () => {
    now += ms
    for (const [id, timer] of [...timers]) if (timer.at <= now) { timers.delete(id); timer.run() }
    await Promise.resolve()
  })
  try {
    changeVisibility('visible')
    const view = render(<App><MCPManagementPanel /></App>)
    assert.equal(pending.length, 1)
    await advance(10000)
    assert.equal(pending.length, 1, 'overlapping poll while request is pending')
    await finish(0)
    await advance(4999)
    assert.equal(pending.length, 1)
    await advance(1)
    assert.equal(pending.length, 2)
    act(() => { changeVisibility('hidden'); changeVisibility('visible'); changeVisibility('hidden') })
    await finish(1)
    assert.equal(timers.size, 0)
    await advance(10000)
    assert.equal(pending.length, 2)
    act(() => changeVisibility('visible'))
    assert.equal(pending.length, 3)
    await finish(2)
    assert.equal(pending.length, 3, 'stale pending marker caused a duplicate visible refresh')
    act(() => changeVisibility('hidden'))
    assert.equal(timers.size, 0)
    fireEvent.click(view.getByRole('button', { name: /刷.*新/ }))
    assert.equal(pending.length, 4, 'manual refresh must work while hidden')
    act(() => { changeVisibility('visible'); changeVisibility('hidden'); changeVisibility('visible') })
    assert.equal(pending.length, 4)
    await finish(3)
    assert.equal(pending.length, 5, 'visible in-flight transitions coalesce into one refresh')
    view.unmount()
    assert.equal(pending[4].signal?.aborted, true)
    await finish(4)
    await advance(10000)
    act(() => changeVisibility('visible'))
    assert.equal(pending.length, 5)
    assert.equal(timers.size, 0)
  } finally {
    cleanup()
    globalThis.setTimeout = nativeSetTimeout
    globalThis.clearTimeout = nativeClearTimeout
    if (visibility) Object.defineProperty(document, 'visibilityState', visibility)
    else Reflect.deleteProperty(document, 'visibilityState')
  }
})

it('shows servers, enables dynamically and reports failed stop without optimistic state', async () => {
  let current = { ...server }
  let fail = false
  const bodies: unknown[] = []
  globalThis.fetch = (async (_url, init) => {
    if (init?.method === 'PUT') {
      const body = JSON.parse(String(init.body)); bodies.push(body)
      if (fail) return Response.json({ code: 105000, message: '测试停用失败' })
      current = { ...current, enabled: body.enabled, status: { state: 'running', tools: ['echo'] } }
      return response(current)
    }
    return response({ items: [current], total: 1, page: 1, page_size: 10 })
  }) as typeof fetch
  const view = render(<App><MCPManagementPanel /></App>)
  await view.findByText('示例服务')
  const toggle = view.getByRole('switch', { name: '启用 示例服务' })
  assert.equal(toggle.getAttribute('aria-checked'), 'false')
  fireEvent.click(toggle)
  await waitFor(() => assert.equal(toggle.getAttribute('aria-checked'), 'true'))
  assert.deepEqual(bodies, [{ enabled: true }])
  assert.ok(view.getByText('运行中'))
  fail = true
  fireEvent.click(toggle)
  await view.findByText('测试停用失败')
  await waitFor(() => assert.equal(toggle.hasAttribute('disabled'), false))
  assert.equal(toggle.getAttribute('aria-checked'), 'true')
})

it('loads complete detail for editing and preserves authentication', async () => {
  let saved: Record<string, unknown> | undefined
  globalThis.fetch = (async (url, init) => {
    if (init?.method === 'PUT') { saved = JSON.parse(String(init.body)); return response({ ...server, ...saved }) }
    if (String(url).includes('/page')) return response({ items: [server], total: 1, page: 1, page_size: 10 })
    return response({ ...server, headers: { Authorization: 'Bearer preserved' } })
  }) as typeof fetch
  const view = render(<App><MCPManagementPanel /></App>)
  await view.findByText('示例服务')
  fireEvent.click(view.getByRole('button', { name: '编辑' }))
  const dialog = await view.findByRole('dialog')
  await waitFor(() => assert.equal((within(dialog).getByLabelText('MCP 名称') as HTMLInputElement).value, '示例服务'))
  fireEvent.change(within(dialog).getByLabelText('MCP 名称'), { target: { value: '更新后的服务' } })
  fireEvent.click(within(dialog).getByRole('button', { name: /保.*存/ }))
  await waitFor(() => assert.equal(saved?.name, '更新后的服务'))
  assert.deepEqual(saved?.headers, { Authorization: 'Bearer preserved' })
})

it('deletes a service only after confirmation', async () => {
  let deleted = false
  globalThis.fetch = (async (_url, init) => {
    if (init?.method === 'DELETE') { deleted = true; return response(null) }
    return response({ items: deleted ? [] : [server], total: deleted ? 0 : 1, page: 1, page_size: 10 })
  }) as typeof fetch
  const view = render(<App><MCPManagementPanel /></App>)
  await view.findByText('示例服务')
  fireEvent.click(view.getByRole('button', { name: '删除' }))
  assert.equal(deleted, false)
  const confirmation = await view.findByText('删除 示例服务？')
  const popup = confirmation.closest('.ant-popconfirm')
  assert.ok(popup)
  fireEvent.click(within(popup as HTMLElement).getByRole('button', { name: /删.*除/ }))
  await waitFor(() => assert.equal(deleted, true))
  await waitFor(() => assert.equal(view.queryByText('示例服务') === null, true))
})

it('formats command arguments, preserves invalid input and saves argument boundaries', async () => {
  let saved: Record<string, unknown> | undefined
  const local = { ...server, transport: 'stdio', command: 'npx', args: ['-y', 'package name'], enabled: true }
  globalThis.fetch = (async (url, init) => {
    if (init?.method === 'PUT') { saved = JSON.parse(String(init.body)); return response({ ...local, ...saved }) }
    if (String(url).includes('/page')) return response({ items: [local], total: 1, page: 1, page_size: 10 })
    return response(local)
  }) as typeof fetch
  const view = render(<App><MCPManagementPanel /></App>)
  await view.findByText('示例服务')
  assert.ok(view.getAllByLabelText('启停说明').length > 0)
  fireEvent.click(view.getByRole('button', { name: '编辑' }))
  const dialog = await view.findByRole('dialog')
  const editor = await within(dialog).findByRole('textbox', { name: '命令参数' }) as HTMLTextAreaElement
  assert.ok(within(dialog).getAllByLabelText('保存说明').length > 0)
  assert.equal(within(dialog).queryByText('保存时会连接新配置；成功后替换原连接，失败时保留原配置。') === null, true)
  fireEvent.change(editor, { target: { value: '["-y",' } })
  fireEvent.click(within(dialog).getByRole('button', { name: '格式化命令参数' }))
  await within(dialog).findByText('命令参数必须是有效 JSON')
  assert.equal(editor.value, '["-y",')
  fireEvent.change(editor, { target: { value: '[1]' } })
  fireEvent.click(within(dialog).getByRole('button', { name: '格式化命令参数' }))
  await within(dialog).findByText('命令参数必须是字符串数组')
  fireEvent.change(editor, { target: { value: '["-y","package name",""]' } })
  fireEvent.click(within(dialog).getByRole('button', { name: '格式化命令参数' }))
  await waitFor(() => assert.equal(editor.value, '[\n  "-y",\n  "package name",\n  ""\n]'))
  assert.equal(editor.getAttribute('aria-invalid'), 'false')
  fireEvent.click(within(dialog).getByRole('button', { name: /保.*存/ }))
  await waitFor(() => assert.deepEqual(saved?.args, ['-y', 'package name', '']))
})
