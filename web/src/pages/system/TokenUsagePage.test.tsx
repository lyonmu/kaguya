/// <reference types="node" />
import { after, afterEach, it } from 'node:test'
import assert from 'node:assert/strict'
import { mock } from 'bun:test'
import { Window } from 'happy-dom'

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

// 图表用桩实现替代 ECharts，直接断言下发给图表的配置。
const options: Record<string, any>[] = []
mock.module('echarts/core', () => ({ init: () => ({ setOption: (option: Record<string, any>) => { options.push(option) }, resize() {}, dispose() {} }), use: () => {} }))
const { render, cleanup, waitFor, act } = await import('@testing-library/react')
const { TokenUsagePage } = await import('./TokenUsagePage')
const originalFetch = globalThis.fetch
const usage = {
  start: '2025-01-01', end: '2025-01-03', activity_start: '2025-09-13', activity_end: '2026-09-12',
  total_tokens: 3700, conversations: 1, peak_tokens: 3600, peak_tokens_date: '2025-01-01', peak_conversations: 1, peak_conversations_date: '2025-01-01',
  days: [{ date: '2025-09-13', total_tokens: 10, conversations: 1 }, { date: '2026-09-12', total_tokens: 500, conversations: 1 }],
  models: [{ id: 'm', name: 'model', provider_id: 'p', provider_name: 'provider', input_tokens: 1, output_tokens: 1, reasoning_tokens: 1, cached_tokens: 1, total_tokens: 4 }],
  providers: [],
}
afterEach(async () => {
  cleanup()
  options.length = 0
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
  globalThis.fetch = originalFetch
})
after(async () => {
  mock.restore()
  await dom.happyDOM.close()
  for (const [key, descriptor] of previous) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor)
    else Reflect.deleteProperty(globalThis, key)
  }
})

it('renders the activity calendar on the fixed window instead of the selected range', async () => {
  const requests: URL[] = []
  globalThis.fetch = (async url => {
    requests.push(new URL(String(url), 'http://localhost'))
    return Response.json({ code: 100000, data: usage })
  }) as typeof fetch
  const page = render(<TokenUsagePage />)
  await waitFor(() => assert.equal(requests.length, 1))
  // 未选日期时不带参数，日历与构成卡片固定，只有汇总卡片随后续时间段变化。
  assert.equal(requests[0].search, '')
  await waitFor(() => assert.deepEqual(options.find(option => option.calendar)?.calendar.range, ['2025-09-13', '2026-09-12']))
  // 热力图按分档着色：空白日期透明，非零日期至少有可见颜色。
  const visualMap = options.find(option => option.calendar)?.visualMap
  assert.equal(visualMap.type, 'piecewise')
  assert.deepEqual(visualMap.pieces[0], { value: 0, color: 'transparent', label: '0' })
  assert.deepEqual(visualMap.pieces.map((piece: { color: string }) => piece.color), ['transparent', '#c6ddff', '#82b8ff'])
  assert.ok(page.getByText(/最近一年 2025-09-13 — 2026-09-12/))
  assert.ok(page.getByText(/全部历史 · 用量最高的 10 项/))
  assert.ok(page.getByText('所选时间段内全部模型累计使用量'))
})
