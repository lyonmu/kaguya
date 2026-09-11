/// <reference types="node" />
import { after, afterEach, it, mock } from 'node:test'
import assert from 'node:assert/strict'
import { Window } from 'happy-dom'

const dom = new Window({ url: 'http://localhost' })
const globals = {
  window: dom, document: dom.document, navigator: dom.navigator,
  HTMLElement: dom.HTMLElement, Element: dom.Element, Node: dom.Node,
  SVGElement: dom.SVGElement, ShadowRoot: dom.ShadowRoot,
  CSSStyleSheet: dom.CSSStyleSheet,
  DOMParser: dom.DOMParser, XMLSerializer: dom.XMLSerializer,
  MutationObserver: dom.MutationObserver, ResizeObserver: dom.ResizeObserver,
  cancelAnimationFrame: dom.cancelAnimationFrame.bind(dom),
  getComputedStyle: dom.getComputedStyle.bind(dom), requestAnimationFrame: dom.requestAnimationFrame.bind(dom), IS_REACT_ACT_ENVIRONMENT: true,
}
const previous = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
const { render, cleanup, waitFor, act, fireEvent } = await import('@testing-library/react')
const { Markdown } = await import('./Markdown')
const { ActivityBlock } = await import('./ActivityBlock')
const { MessageList: RuntimeMessageList } = await import('./MessageList')
const { Composer } = await import('./Composer')
const { useState } = await import('react')
const { AssistantThread } = await import('./AssistantThread')
function MessageList(props: React.ComponentProps<typeof RuntimeMessageList>) {
  return <AssistantThread turns={props.turns} streaming={props.streaming} disabled={false} onSend={async () => {}} onStop={() => {}}><RuntimeMessageList {...props} /></AssistantThread>
}
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

it('loads Markdown images only on request and requires approval for a changed URL', () => {
  const view = render(<Markdown text="![预览](https://example.com/image.png?secret=value)" />)
  assert.equal(view.container.querySelector('img'), null)
  fireEvent.click(view.getByRole('button', { name: '加载图片：预览' }))
  assert.equal(view.container.querySelector('img')?.getAttribute('src'), 'https://example.com/image.png?secret=value')
  assert.equal(view.container.querySelector('img')?.getAttribute('referrerpolicy'), 'no-referrer')
  view.rerender(<Markdown text="![预览](https://example.com/other.png)" />)
  assert.equal(view.container.querySelector('img'), null)
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

it('keeps tool display read-only and exposes separate local copy actions', async () => {
  let copied = ''
  const write = navigator.clipboard.writeText
  navigator.clipboard.writeText = async text => { copied = text }
  try {
    const view = render(<ActivityBlock block={{ type: 'tool_call', phase: 'block_end', tool_name: 'bash', input: '{"command":"go test ./..."}', output: { type: 'text', text: 'ok' } }} />)
    const details = view.container.querySelector('details')!
    await act(async () => { details.open = true; fireEvent(details, new dom.Event('toggle') as unknown as Event) })
    const actions = view.getByLabelText('执行命令本地操作')
    assert.equal(actions.querySelectorAll('button').length, 2)
    await act(async () => { fireEvent.click(view.getByRole('button', { name: '复制命令' })) })
    assert.equal(copied, 'go test ./...')
    await act(async () => { fireEvent.click(view.getByRole('button', { name: '复制输出' })) })
    assert.equal(copied, 'ok')
  } finally { navigator.clipboard.writeText = write }
})

it('uses Ant Design X Mermaid after streaming and recovers from invalid source', async () => {
  // Exercise parsing and component lifecycle here; interactive layout is verified in a browser.
  const mermaid = (await import('mermaid')).default
  const rendering = mock.method(mermaid, 'render', async (_id: string, code: string) => {
    await mermaid.parse(code)
    return { svg: '<svg xmlns="http://www.w3.org/2000/svg" width="100%" viewBox="0 0 1200 400"><foreignObject><div xmlns="http://www.w3.org/1999/xhtml">Prometheus<br>指标</div></foreignObject></svg>', diagramType: 'flowchart' }
  })
  try {
    const source = '```mermaid\nflowchart LR\n A[Docker] --> B[Prometheus]\n```'
    const view = render(<Markdown text={'```mermaid\nflowchart LR\n A['} streaming />)
    assert.equal(view.queryByText('图表生成中…'), null)
    assert.equal(view.getByLabelText('mermaid 代码').textContent, 'flowchart LR\n A[')
    assert.equal(rendering.mock.callCount(), 0)
    view.rerender(<Markdown text={source} />)
    await waitFor(() => assert.ok(view.container.querySelector('.ant-mermaid-graph svg')), { timeout: 10000 })
    assert.ok(view.container.querySelector('.ant-mermaid'))
    assert.ok(view.getByText('图片'))
    assert.ok(view.getByText('代码'))
    view.rerender(<Markdown text={source + '\n\n说明文字'} />)
    assert.ok(view.container.querySelector('.ant-mermaid-graph svg'))
    view.rerender(<Markdown text={'```mermaid\nnot a diagram\n```'} />)
    await view.findByText('图表无法渲染，请检查 Mermaid 源码。')
    view.rerender(<Markdown text={'```mermaid\nsequenceDiagram\n participant A as 客户端\n participant B as 服务端\n A->>B: 请求\n B-->>A: 响应\n```'} />)
    await waitFor(() => assert.ok(view.container.querySelector('.ant-mermaid-graph svg')), { timeout: 10000 })
    assert.equal(view.queryByText('图表无法渲染，请检查 Mermaid 源码。'), null)
  } finally {
    rendering.mock.restore()
  }
})

it('embeds selected project files inline and removes references when their tokens are deleted', async () => {
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
    const [references, setReferences] = useState<string[]>([])
    return <AssistantThread turns={[]} streaming={false} disabled={false} onSend={async () => { sends++ }} onStop={() => {}}><Composer projectId="42" value={value} onChange={setValue} references={references} onReferencesChange={setReferences} modelId="" onModelChange={() => {}} streaming={false} disabled={false} /></AssistantThread>
  }
  const view = render(<Draft />)
  assert.ok(view.getByLabelText('完全访问当前项目'))
  const textarea = view.getByLabelText('对话消息') as HTMLTextAreaElement
  fireEvent.change(textarea, { target: { value: '看看 @', selectionStart: 4 } })
  await waitFor(() => assert.equal(view.getAllByRole('option').length, 2))
  // Browsers scroll the highlighted option; happy-dom has no layout implementation.
  view.getAllByRole('option').forEach(option => { option.scrollIntoView = () => {} })
  fireEvent.keyDown(textarea, { key: 'ArrowDown' })
  fireEvent.keyDown(textarea, { key: 'Enter' })
  await waitFor(() => assert.equal(textarea.value, '看看 @docs/my file.md '))
  assert.equal(sends, 0)
  assert.equal(view.queryByRole('listbox', { name: '项目文件' }), null)
  assert.equal(view.queryByLabelText('已引用文件'), null)
  assert.equal(textarea.value.includes('"'), false)
  assert.ok(searches[0].includes('/42/files'))
  fireEvent.change(textarea, { target: { value: '看看 ', selectionStart: 3 } })
  fireEvent.change(textarea, { target: { value: '@main', selectionStart: 5 } })
  await waitFor(() => assert.ok(view.getByRole('listbox', { name: '项目文件' })))
  fireEvent.keyDown(textarea, { key: 'Escape' })
  assert.equal(view.queryByRole('listbox', { name: '项目文件' }), null)
  fireEvent.keyDown(textarea, { key: 'Enter', shiftKey: true })
  assert.equal(sends, 0)
  await act(async () => { fireEvent.keyDown(textarea, { key: 'Enter' }) })
  await waitFor(() => assert.equal(sends, 1))
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

it('loads folded history details only on expansion and reuses them while mounted', async () => {
  let requests = 0
  globalThis.fetch = (async url => {
    requests++
    assert.ok(String(url).endsWith('/conversation/123/turns/6/blocks/2'))
    return Response.json({ code: 100000, data: { type: 'tool_call', sequence: 2, tool_name: 'bash', input: '{"command":"go test ./..."}', output: { type: 'text', text: 'test output marker' } } })
  }) as typeof fetch
  const view = render(<ActivityBlock conversationId="123" turnIndex={6} block={{ type: 'tool_call', tool_name: 'bash', sequence: 2, details_deferred: true, has_output: true }} />)
  assert.equal(requests, 0)
  assert.ok(view.getByText('完成'))
  const details = view.container.querySelector('details')!
  await act(async () => { details.open = true; fireEvent(details, new dom.Event('toggle') as unknown as Event) })
  await view.findByText('test output marker')
  assert.equal(requests, 1)
  await act(async () => { details.open = false; fireEvent(details, new dom.Event('toggle') as unknown as Event) })
  await act(async () => { details.open = true; fireEvent(details, new dom.Event('toggle') as unknown as Event) })
  assert.equal(requests, 1)
})

it('cancels folded detail requests on close and allows explicit retry after failure', async () => {
  let signal: AbortSignal | undefined
  let resolve!: (value: Response) => void
  let requests = 0
  globalThis.fetch = (async (_url, init) => {
    requests++
    signal = init?.signal as AbortSignal
    if (requests === 1) return new Promise<Response>(done => { resolve = done })
    if (requests === 2) return Response.json({ code: 102001, message: '详情暂不可用' }, { status: 503 })
    return Response.json({ code: 100000, data: { type: 'reasoning', sequence: 1, text: 'saved reasoning marker' } })
  }) as typeof fetch
  const view = render(<ActivityBlock conversationId="123" turnIndex={1} block={{ type: 'reasoning', sequence: 1, details_deferred: true }} />)
  const details = view.container.querySelector('details')!
  await act(async () => { details.open = true; fireEvent(details, new dom.Event('toggle') as unknown as Event) })
  assert.equal(signal?.aborted, false)
  await act(async () => { details.open = false; fireEvent(details, new dom.Event('toggle') as unknown as Event) })
  assert.equal(signal?.aborted, true)
  await act(async () => { resolve(Response.json({ code: 100000, data: { type: 'reasoning', text: 'stale detail' } })) })
  await act(async () => { details.open = true; fireEvent(details, new dom.Event('toggle') as unknown as Event) })
  await view.findByText('详情暂不可用')
  fireEvent.click(view.getByRole('button', { name: '重试加载详情' }))
  await view.findByText('saved reasoning marker')
  assert.equal(view.queryByText('stale detail'), null)
  assert.equal(requests, 3)
})
