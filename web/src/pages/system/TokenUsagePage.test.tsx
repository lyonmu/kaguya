/// <reference types="node" />
import { after, afterEach, it } from 'node:test'
import assert from 'node:assert/strict'
import { mock } from 'bun:test'
import { Window } from 'happy-dom'

const dom = new Window({ url: 'http://localhost' })
// 用量页通过 ResizeObserver 计算构成图标签宽度；测试里显式触发回调，保证断言不受布局实现影响。
const resizeObservers: TestResizeObserver[] = []
class TestResizeObserver {
  callback: (entries: { contentRect: { width: number } }[]) => void
  targets: Element[] = []
  constructor(callback: (entries: { contentRect: { width: number } }[]) => void) {
    this.callback = callback
    resizeObservers.push(this)
  }
  observe(target: Element) { this.targets.push(target) }
  unobserve() {}
  disconnect() {}
}
const globals = {
  window: dom, document: dom.document, navigator: dom.navigator,
  HTMLElement: dom.HTMLElement, Element: dom.Element, Node: dom.Node,
  SVGElement: dom.SVGElement, ShadowRoot: dom.ShadowRoot,
  MutationObserver: dom.MutationObserver, ResizeObserver: TestResizeObserver,
  getComputedStyle: dom.getComputedStyle.bind(dom), IS_REACT_ACT_ENVIRONMENT: true,
}
const previous = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })

// 图表用桩实现替代 ECharts，直接断言下发给图表的配置。
const options: Record<string, any>[] = []
mock.module('echarts/core', () => ({ init: () => ({ setOption: (option: Record<string, any>) => { options.push(option) }, resize() {}, dispose() {} }), use: () => {} }))
const { render, cleanup, waitFor, act } = await import('@testing-library/react')
const { theme } = await import('antd')
const { TokenUsagePage } = await import('./TokenUsagePage')
const originalFetch = globalThis.fetch
const usage = {
  start: '2025-01-01', end: '2025-01-03', activity_start: '2025-09-13', activity_end: '2026-09-12',
  total_tokens: 3700, conversations: 1, peak_tokens: 3600, peak_tokens_date: '2025-01-01', peak_conversations: 1, peak_conversations_date: '2025-01-01',
  days: [{ date: '2025-09-13', total_tokens: 10, conversations: 1 }, { date: '2026-09-12', total_tokens: 500, conversations: 1 }],
  models: [{ id: 'm', name: 'model', provider_id: 'p', provider_name: 'provider', input_tokens: 1, output_tokens: 1, reasoning_tokens: 1, cached_tokens: 1, total_tokens: 4 }],
  providers: [],
}
async function resizeComposition(width: number) {
  const observers = resizeObservers.filter(observer => observer.targets.some(target => target.classList.contains('token-usage-composition')))
  assert.equal(observers.length, 1)
  await act(async () => { observers[0].callback([{ contentRect: { width } }]) })
}
afterEach(async () => {
  cleanup()
  options.length = 0
  resizeObservers.length = 0
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
  // 热力图按分档着色：空白日期使用主题弱填充色（不能透明，否则明亮模式下会与卡片背景重合），非零日期至少有可见颜色。
  const emptyColor = theme.getDesignToken().colorFillSecondary
  const visualMap = options.find(option => option.calendar)?.visualMap
  assert.equal(visualMap.type, 'piecewise')
  assert.deepEqual(visualMap.pieces[0], { value: 0, color: emptyColor, label: '0' })
  assert.deepEqual(visualMap.pieces.map((piece: { color: string }) => piece.color), [emptyColor, '#c6ddff', '#82b8ff'])
  assert.ok(page.getByText(/最近一年 2025-09-13 — 2026-09-12/))
  assert.ok(page.getByText(/全部历史 · 用量最高的 10 项/))
  assert.ok(page.getByText('所选时间段内全部模型累计使用量'))
  // Token 构成按模型分布只显示模型名（不含厂商），标签缩小字号并限宽截断，柱子收窄避免类目变多后互相挤占。
  const composition = options.find(option => option.series?.[0]?.type === 'bar')
  assert.deepEqual(composition?.xAxis.data, ['model'])
  assert.equal(composition?.xAxis.axisLabel.fontSize, 11)
  // 尚未测到容器宽度时使用默认标签宽度；实测后由 compositionLabelWidth 按容器宽度分档。
  assert.equal(composition?.xAxis.axisLabel.width, 84)
  assert.equal(composition?.series[0].barMaxWidth, 36)
  // 不设固定最小宽度，构成图随卡片宽度自适应。
  assert.equal(page.container.querySelector<HTMLElement>('.token-usage-composition')?.style.minWidth, '')
})

it('fits composition labels to the measured container width', async () => {
  const models = Array.from({ length: 10 }, (_, index) => ({ ...usage.models[0], id: `m${index}`, name: `model-${index}` }))
  globalThis.fetch = (async () => Response.json({ code: 100000, data: { ...usage, models } })) as typeof fetch
  render(<TokenUsagePage />)
  await waitFor(() => assert.ok(options.find(option => option.series?.[0]?.type === 'bar')))
  // 容器变窄时标签逐档收窄截断，不再靠固定最小宽度把页面撑出横向滚动。
  await resizeComposition(1005)
  assert.equal(options.at(-1)?.xAxis.axisLabel.width, 84)
  await resizeComposition(663)
  assert.equal(options.at(-1)?.xAxis.axisLabel.width, 48)
  await resizeComposition(1312)
  assert.equal(options.at(-1)?.xAxis.axisLabel.width, 112)
})
