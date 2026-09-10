/// <reference types="node" />
import { after, afterEach, it } from 'node:test'
import assert from 'node:assert/strict'
import { Window } from 'happy-dom'

const dom = new Window({ url: 'http://localhost' })
const globals = {
  window: dom, document: dom.document, navigator: dom.navigator,
  HTMLElement: dom.HTMLElement, Element: dom.Element, Node: dom.Node,
  SVGElement: dom.SVGElement, ShadowRoot: dom.ShadowRoot,
  MutationObserver: dom.MutationObserver, ResizeObserver: dom.ResizeObserver,
  getComputedStyle: dom.getComputedStyle.bind(dom), requestAnimationFrame: dom.requestAnimationFrame.bind(dom), IS_REACT_ACT_ENVIRONMENT: true,
}
const previous = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
const { render, cleanup, waitFor, act, fireEvent } = await import('@testing-library/react')
const { Markdown } = await import('./Markdown')
const { ActivityBlock } = await import('./ActivityBlock')
const { MessageList } = await import('./MessageList')
const { Composer } = await import('./Composer')
const { useState } = await import('react')
const originalFetch = globalThis.fetch
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


it('renders safe GFM and copies exact code with success and failure feedback', async () => {
  let copied = ''
  const write = navigator.clipboard.writeText
  navigator.clipboard.writeText = async text => { copied = text }
  try {
    const view = render(<Markdown text={'```go\nfmt.Println("<script>hi</script>")\n```\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\n`inline`\n\n<script>alert(1)</script>'} />)
    await act(async () => { fireEvent.click(view.getByLabelText('复制代码')) })
    assert.equal(copied, 'fmt.Println("<script>hi</script>")')
    assert.ok(view.getByText('已复制'))
    assert.equal(view.container.querySelectorAll('script').length, 0)
    assert.ok(view.getByRole('table'))
    await waitFor(() => assert.ok(view.container.querySelector('.hljs-string')))
    assert.equal(view.container.querySelectorAll('script').length, 0)
    navigator.clipboard.writeText = async () => { throw new Error('denied') }
    await act(async () => { fireEvent.click(view.getByLabelText('复制代码')) })
    assert.ok(view.getByText('复制失败'))
  } finally { navigator.clipboard.writeText = write }
})

it('distinguishes execution from completed input, failures, and interrupted calls', () => {
  const block = { type: 'tool_call' as const, phase: 'block_end' as const, tool_name: 'bash', input: '{"command":"go test ./..."}' }
  const view = render(<ActivityBlock block={block} streaming />)
  assert.ok(view.getByText('执行中'))
  assert.equal(view.container.querySelector('details')?.open, false)
  view.rerender(<ActivityBlock block={{ ...block, output: { type: 'text', text: 'ok' } }} streaming />)
  assert.ok(view.getByText('完成'))
  view.rerender(<ActivityBlock block={{ ...block, output: { type: 'error', text: 'exit 1' } }} />)
  assert.ok(view.getByText('失败'))
  view.rerender(<ActivityBlock block={{ ...block, input: 'null' }} />)
  assert.ok(view.getByText('未完成'))
})

it('searches project files, inserts quoted paths by keyboard, and dismisses on Escape', async () => {
  const searches: string[] = []
  globalThis.fetch = (async url => {
    if (String(url).includes('/files')) {
      searches.push(String(url))
      return Response.json({ code: 100000, data: { files: ['src/main.go', 'docs/my file.md'], truncated: false } })
    }
    return Response.json({ code: 100000, data: [] })
  }) as typeof fetch
  let sends = 0
  function Draft() {
    const [value, setValue] = useState('')
    return <Composer projectId="42" value={value} onChange={setValue} modelId="" onModelChange={() => {}} streaming={false} disabled={false} onSend={() => { sends++ }} onStop={() => {}} />
  }
  const view = render(<Draft />)
  const textarea = view.getByLabelText('对话消息') as HTMLTextAreaElement
  fireEvent.change(textarea, { target: { value: '看看 @', selectionStart: 4 } })
  await waitFor(() => assert.equal(view.getAllByRole('option').length, 2))
  // Browsers scroll the highlighted option; happy-dom has no layout implementation.
  view.getAllByRole('option').forEach(option => { option.scrollIntoView = () => {} })
  fireEvent.keyDown(textarea, { key: 'ArrowDown' })
  fireEvent.keyDown(textarea, { key: 'Enter' })
  await waitFor(() => assert.equal(textarea.value, '看看 @"docs/my file.md" '))
  assert.equal(sends, 0)
  assert.equal(view.queryByRole('listbox', { name: '项目文件' }), null)
  assert.ok(view.getByLabelText('已引用文件'))
  assert.ok(searches[0].includes('/42/files'))
  fireEvent.change(textarea, { target: { value: '@main', selectionStart: 5 } })
  await waitFor(() => assert.ok(view.getByRole('listbox', { name: '项目文件' })))
  fireEvent.keyDown(textarea, { key: 'Escape' })
  assert.equal(view.queryByRole('listbox', { name: '项目文件' }), null)
  fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: true })
  assert.equal(sends, 0)
  fireEvent.keyDown(textarea, { key: 'Enter' })
  assert.equal(sends, 1)
})

it('offers continuation only on the last saved page and never while streaming', () => {
  const turn = { turn_index: 1, user_content: 'task', model_id: 'm', model_name: 'model', api_protocol: 'openai', started_at: new Date().toISOString(), blocks: [], finish_reason: 'step_limit', duration_ms: 1, tool_calls: 0, usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2, cached_tokens: 0, reasoning_tokens: 0 } }
  let continued = 0
  const props = { turns: [turn], loading: false, streaming: false, page: 1, totalPages: 2, initialEnd: false, onPageChange: async () => {}, onContinue: () => { continued++ } }
  const view = render(<MessageList {...props} />)
  assert.equal(view.queryByRole('button', { name: '继续执行 →' }), null)
  view.rerender(<MessageList {...props} page={2} />)
  fireEvent.click(view.getByRole('button', { name: '继续执行 →' }))
  assert.equal(continued, 1)
  view.rerender(<MessageList {...props} page={2} streaming />)
  assert.equal((view.getByRole('button', { name: '继续执行 →' }) as HTMLButtonElement).disabled, true)
})
