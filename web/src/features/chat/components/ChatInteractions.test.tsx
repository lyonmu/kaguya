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
import type { Reference } from '../types'
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

it('keeps Markdown image URL compatibility while rejecting dangerous protocols', () => {
  const view = render(<Markdown text="![危险](javascript:alert(1))" />)
  assert.equal(view.queryByRole('button', { name: '加载图片：危险' }), null)
  assert.equal(view.container.querySelector('img'), null)
  for (const source of ['http://example.com/a.png', '/relative.png', '//cdn.example.com/a.png']) {
    view.rerender(<Markdown text={`![兼容](${source})`} />)
    const button = view.getByRole('button', { name: '加载图片：兼容' })
    assert.equal(button.getAttribute('title'), source)
    fireEvent.click(button)
    assert.equal(view.container.querySelector('img')?.getAttribute('src'), source)
  }
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
    // 桌面应用不提供 PNG 下载，只保留缩放与重置。
    assert.equal(view.container.querySelector('.anticon-download'), null)
    assert.ok(view.container.querySelector('.anticon-zoom-in'))
    assert.ok(view.container.querySelector('.anticon-zoom-out'))
    assert.ok(view.getByText('重置'))
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
    const [references, setReferences] = useState<Reference[]>([])
    return <AssistantThread turns={[]} streaming={false} disabled={false} onSend={async () => { sends++ }} onStop={() => {}}><Composer projectId="42" value={value} onChange={setValue} references={references} onReferencesChange={setReferences} modelId="" onModelChange={() => {}} permissionMode="full_access" onPermissionChange={() => {}} streaming={false} disabled={false} /></AssistantThread>
  }
  const view = render(<Draft />)
  assert.ok(view.container.querySelector('.chat-access-mode'))
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

it('copies the user question from its own message', async () => {
  let copied = ''
  const write = navigator.clipboard.writeText
  navigator.clipboard.writeText = async text => { copied = text }
  try {
    const turn = { turn_index: 1, user_content: '帮我看看这条命令', model_id: 'm', model_name: 'model', api_protocol: 'openai', started_at: new Date().toISOString(), blocks: [], finish_reason: 'stop', duration_ms: 1, tool_calls: 0, usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2, cached_tokens: 0, cache_creation_tokens: 0, reasoning_tokens: 0 } }
    const view = render(<MessageList turns={[turn]} loading={false} streaming={false} page={1} totalPages={1} initialEnd onPageChange={async () => {}} />)
    await act(async () => { fireEvent.click(view.getByLabelText('复制提问')) })
    assert.equal(copied, '帮我看看这条命令')
    assert.ok(view.getByText('已复制'))
  } finally { navigator.clipboard.writeText = write }
})

it('keeps completed messages automatic without manual memory actions', () => {
  const turn = { id: 'turn-7', turn_index: 1, user_content: '这个项目继续用 SQLCipher', model_id: 'm', model_name: 'model', api_protocol: 'openai', started_at: new Date().toISOString(), blocks: [{ type: 'text' as const, sequence: 1, text: '好的，保持 SQLCipher。' }], finish_reason: 'stop', duration_ms: 1, tool_calls: 0, usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2, cached_tokens: 0, cache_creation_tokens: 0, reasoning_tokens: 0 } }
  const view = render(<MessageList turns={[turn]} loading={false} streaming={false} page={1} totalPages={1} initialEnd onPageChange={async () => {}} />)
  assert.equal(view.queryByText('保存为记忆'), null)
  assert.ok(view.getByLabelText('复制提问'))
  assert.ok(view.getByLabelText('复制回答'))
})

it('offers continuation only on the last saved page and never while streaming', () => {
  const turn = { turn_index: 1, user_content: 'task', model_id: 'm', model_name: 'model', api_protocol: 'openai', started_at: new Date().toISOString(), blocks: [], finish_reason: 'step_limit', duration_ms: 1, tool_calls: 0, usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2, cached_tokens: 0, cache_creation_tokens: 0, reasoning_tokens: 0 } }
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

it('offers typed file and directory reference candidates', async () => {
  globalThis.fetch = (async url => {
    if (String(url).includes('/files')) {
      return Response.json({ code: 100000, data: { files: ['src/main.go'], items: [{ type: 'file', path: 'src/main.go', name: 'main.go' }, { type: 'directory', path: 'src', name: 'src' }], truncated: false } })
    }
    return Response.json({ code: 100000, data: [] })
  }) as typeof fetch
  let selected: Reference[] = []
  function Draft() {
    const [value, setValue] = useState('')
    const [references, setReferences] = useState<Reference[]>([])
    selected = references
    return <AssistantThread turns={[]} streaming={false} disabled={false} onSend={async () => {}} onStop={() => {}}><Composer projectId="42" value={value} onChange={setValue} references={references} onReferencesChange={setReferences} modelId="" onModelChange={() => {}} permissionMode="full_access" onPermissionChange={() => {}} streaming={false} disabled={false} /></AssistantThread>
  }
  const view = render(<Draft />)
  const textarea = view.getByLabelText('对话消息') as HTMLTextAreaElement
  fireEvent.change(textarea, { target: { value: '看看 @s', selectionStart: 5 } })
  await waitFor(() => assert.equal(view.getAllByRole('option').length, 2))
  const options = view.getAllByRole('option')
  options.forEach(option => { option.scrollIntoView = () => {} })
  fireEvent.click(options[1])
  await waitFor(() => assert.deepEqual(selected, [{ type: 'directory', path: 'src' }]))
  await waitFor(() => assert.ok(view.getByText(/目录 · src/)))
})

it('loads subsequent candidates with the keyboard, keeps focus, and labels the project root', async () => {
  const cursors: (string | null)[] = []
  globalThis.fetch = (async url => {
    const parsed = new URL(String(url), 'http://localhost')
    if (parsed.pathname.endsWith('/files')) {
      const cursor = parsed.searchParams.get('cursor')
      cursors.push(cursor)
      return Response.json({ code: 100000, data: cursor
        ? { files: ['b'], items: [{ type: 'file', path: 'b' }], truncated: true, scan_incomplete: true }
        : { files: ['a'], items: [{ type: 'directory', path: '.', name: '.' }, { type: 'file', path: 'a' }], truncated: true, next_cursor: 'page-2', scan_incomplete: false } })
    }
    return Response.json({ code: 100000, data: [] })
  }) as typeof fetch
  let selected: Reference[] = []
  function Draft() {
    const [value, setValue] = useState('')
    const [references, setReferences] = useState<Reference[]>([])
    selected = references
    return <AssistantThread turns={[]} streaming={false} disabled={false} onSend={async () => {}} onStop={() => {}}><Composer projectId="42" value={value} onChange={setValue} references={references} onReferencesChange={setReferences} modelId="" onModelChange={() => {}} permissionMode="full_access" onPermissionChange={() => {}} streaming={false} disabled={false} /></AssistantThread>
  }
  const view = render(<Draft />)
  const textarea = view.getByLabelText('对话消息') as HTMLTextAreaElement
  fireEvent.change(textarea, { target: { value: '@', selectionStart: 1 } })
  await waitFor(() => assert.equal(view.getAllByRole('option').length, 2))
  assert.ok(view.getByText('项目根目录'))
  const more = view.getByRole('button', { name: '加载更多' })
  more.scrollIntoView = () => {}
  // 转移焦点到可Tab访问的loadMore不能把popover先卸载。
  fireEvent.blur(textarea, { relatedTarget: more })
  assert.ok(view.getByRole('button', { name: '加载更多' }))
  fireEvent.focus(textarea)
  fireEvent.keyDown(textarea, { key: 'ArrowUp' })
  assert.equal(textarea.getAttribute('aria-activedescendant'), 'mention-load-more')
  fireEvent.keyDown(textarea, { key: 'Enter' })
  await waitFor(() => assert.deepEqual(cursors, [null, 'page-2']))
  await waitFor(() => assert.equal(view.getAllByRole('option').length, 3))
  assert.deepEqual(selected, [])
  assert.equal(view.queryByRole('button', { name: '加载更多' }), null)
  assert.ok(view.getByText('扫描未完成，请输入更具体的路径'))
  view.getAllByRole('option').forEach(option => { option.scrollIntoView = () => {} })
  fireEvent.keyDown(textarea, { key: 'ArrowUp' })
  fireEvent.keyDown(textarea, { key: 'ArrowUp' })
  fireEvent.keyDown(textarea, { key: 'Enter' })
  await waitFor(() => assert.deepEqual(selected, [{ type: 'directory', path: '.' }]))
})

it('shows a pending approval once and reports one decision', async () => {
  const decisions: [string, string][] = []
  const { ApprovalCard } = await import('./ApprovalCard')
  const approval = { approval_id: 'a1', tool_call_id: 'c1', tool_name: 'write', effect: 'mutating', mode: 'full_approval', status: 'pending', input: '{"path":"a.txt"}' }
  const view = render(<ApprovalCard approval={approval} onDecide={async (id, decision) => { decisions.push([id, decision]) }} />)
  assert.ok(view.getByText('write'))
  assert.ok(view.getByText('写入'))
  fireEvent.click(view.getByRole('button', { name: /批准一次/ }))
  await waitFor(() => assert.deepEqual(decisions, [['a1', 'approve']]))
  view.rerender(<ApprovalCard approval={{ ...approval, status: 'approved' }} onDecide={async () => {}} />)
  assert.equal(view.queryByRole('button', { name: /批准一次/ }), null)
})

it('distinguishes approval from execution and shows audit cards without replay buttons', async () => {
  const { ApprovalCard } = await import('./ApprovalCard')
  const approval = { approval_id: 'audit', tool_call_id: 'call', tool_name: 'write', effect: 'mutating', mode: 'full_approval', status: 'approved' }
  const view = render(<ApprovalCard approval={approval} />)
  assert.ok(view.getByText('已批准'))
  assert.equal(view.queryByText('已批准并执行'), null)
  view.rerender(<ApprovalCard approval={{ ...approval, status: 'consumed' }} />)
  assert.ok(view.getByText('已授权，执行结果未知'))
  view.rerender(<ApprovalCard approval={{ ...approval, status: 'consumed', execution_state: 'succeeded' }} />)
  assert.ok(view.getByText('工具返回成功'))
  view.rerender(<ApprovalCard approval={{ ...approval, status: 'consumed', execution_state: 'canceled' }} />)
  assert.ok(view.getByText('执行已取消，可能已有副作用'))
  view.rerender(<ApprovalCard approval={{ ...approval, status: 'consumed', execution_state: 'canceled', execution_reason_code: 'canceled_before_execution' }} />)
  assert.ok(view.getByText('执行前已取消'))
  view.rerender(<ApprovalCard approval={{ ...approval, status: 'pending' }} />)
  assert.equal(view.queryByRole('button', { name: /批准一次/ }), null)
})

it('shows reported preparation consumption and unknown calls for a failed turn', () => {
  const turn = { turn_index: 1, user_content: 'inspect file', model_id: 'm', model_name: 'model', api_protocol: 'openai', started_at: new Date().toISOString(), blocks: [], duration_ms: 1, tool_calls: 0, status: 'failed' as const,
    preparation_usage: { version: 1 as const, summary_calls: 2, reported_calls: 1, unreported_calls: 1,
      usage: { input_tokens: 10, output_tokens: 20, total_tokens: 42, cache_hit_tokens: 5, cache_creation_tokens: 7, reasoning_tokens: 3 } } }
  const view = render(<MessageList turns={[turn]} loading={false} streaming={false} page={1} totalPages={1} initialEnd onPageChange={async () => {}} />)
  assert.ok(view.getByText(/摘要准备已报告 42 tokens/))
  assert.ok(view.getByText(/另有 1 次调用用量未知/))
  assert.ok(view.getByText(/未计入已完成轮次累计用量/))
  view.rerender(<MessageList turns={[{ ...turn, status: 'completed' }]} loading={false} streaming={false} page={1} totalPages={1} initialEnd onPageChange={async () => {}} />)
  assert.equal(view.queryByText(/摘要准备已报告/), null)
})

it('shows requested reference intent safely and restores choices without replaying', async () => {
  const { referenceSummaryGuard } = await import('../referenceSummary')
  const path = '<img src=x onerror=alert(1)>'
  const checked = referenceSummaryGuard({ preparation_state: 'requested', sources: [{ type: 'directory', path }] })
  assert.ok(checked.ok)
  const turn = { turn_index: 1, user_content: 'inspect', started_at: new Date().toISOString(), blocks: [], model_name: '', model_id: '', api_protocol: '', duration_ms: 0, tool_calls: 0, status: 'failed' as const, reference_summary: checked.value }
  let restores = 0
  const view = render(<MessageList turns={[turn]} loading={false} streaming={false} page={1} totalPages={1} initialEnd canRestoreReferences onRestoreReferences={async item => { assert.equal(item, turn); restores++ }} onPageChange={async () => {}} />)
  assert.ok(view.getByText(/仅记录选择，尚未解析/))
  assert.ok(view.getByText(`目录：${path}`))
  assert.equal(view.container.querySelector('[onerror]'), null)
  view.container.querySelector('details')!.setAttribute('open', '')
  fireEvent.click(view.getByRole('button', { name: /恢复引用选择/ }))
  await waitFor(() => assert.equal(restores, 1))
  view.rerender(<MessageList turns={[{ ...turn, status: 'completed' }]} loading={false} streaming={false} page={1} totalPages={1} initialEnd canRestoreReferences onRestoreReferences={async () => { restores++ }} onPageChange={async () => {}} />)
  assert.equal(view.queryByRole('button', { name: /恢复引用选择/ }), null)
})

it('distinguishes body truncation from incomplete scanning and unknown model capacity', async () => {
  const { ReferenceDetails } = await import('./ReferenceDetails')
  const { referenceSummaryGuard } = await import('../referenceSummary')
  const checked = referenceSummaryGuard({ preparation_state: 'resolved', partial_reason: 'context_budget', budget_known: false, sources: [{ type: 'directory', path: '.', included: true, hash: 'b'.repeat(16), files: [{ path: 'a.go', included: true, status: 'included', ranges: [{ start: 1, end: 2 }], hash: 'a'.repeat(64), next_line: 3 }] }] })
  assert.ok(checked.ok)
  const view = render(<ReferenceDetails summary={checked.value} />)
  assert.ok(view.getByText(/部分资料已包含.*模型窗口未知/))
  assert.ok(view.getByText(/目录扫描未标记为未完成/))
  assert.ok(view.getByText(/实际行：1–2/))
  assert.ok(view.getByText(/下一行 3/))
  assert.equal(view.queryByText('目录扫描未完成'), null)
  const legacy = { ...checked.value, preparation_state: undefined }
  view.rerender(<ReferenceDetails summary={legacy} />)
  assert.ok(view.getByText(/旧记录，解析阶段未知/))
})


it('keeps exact untrusted approval parameters readable and preserves retry after a failed decision', async () => {
  const { ApprovalCard } = await import('./ApprovalCard')
  const input = JSON.stringify({ command: 'printf "<img src=x onerror=alert(1)>"', timeout: 30 })
  let calls = 0
  const view = render(<ApprovalCard approval={{ approval_id: 'a1', tool_call_id: 'c1', tool_name: 'bash', effect: 'unknown', mode: 'full_approval', status: 'pending', input }} onDecide={async () => { calls++; if (calls === 1) throw new Error('审批请求失败') }} />)
  assert.equal(view.container.querySelector('pre')?.textContent, JSON.stringify(JSON.parse(input), null, 2))
  assert.equal(view.container.querySelector('img'), null)
  fireEvent.click(view.getByRole('button', { name: /批准一次/ }))
  await waitFor(() => assert.ok(view.getByText('审批请求失败')))
  fireEvent.click(view.getByRole('button', { name: /拒绝/ }))
  await waitFor(() => assert.equal(calls, 2))
})

it('moves resolved live approvals into the latest message once and keeps historical results authoritative', () => {
  const approval = { approval_id: 'audit-live', tool_call_id: 'call', tool_name: 'bash', effect: 'unknown', mode: 'full_approval', status: 'pending' }
  const turn = { turn_index: 2, user_content: 'inspect', model_id: 'm', model_name: 'model', api_protocol: 'openai', started_at: new Date().toISOString(), blocks: [], duration_ms: 1, tool_calls: 1, status: 'completed' as const, approvals: [approval] }
  const props = { turns: [turn], loading: false, streaming: false, page: 1, totalPages: 1, initialEnd: true, onPageChange: async () => {} }
  const view = render(<MessageList {...props} liveApprovals={[approval]} activeApprovalIds={['audit-live']} />)
  assert.equal(view.queryByText('等待批准'), null)
  const resolved = { ...approval, status: 'consumed', execution_state: 'succeeded' as const }
  view.rerender(<MessageList {...props} liveApprovals={[resolved]} />)
  assert.equal(view.getAllByText('工具返回成功').length, 1)
  assert.equal(view.container.querySelector('details.chat-approval-record')?.hasAttribute('open'), false)
  assert.equal(view.queryByRole('button', { name: /批准一次/ }), null)
  view.rerender(<MessageList {...props} turns={[{ ...turn, approvals: [resolved] }]} liveApprovals={[approval]} />)
  assert.ok(view.getByText('工具返回成功'))
  view.rerender(<MessageList {...props} page={1} totalPages={2} turns={[{ ...turn, approvals: [] }]} liveApprovals={[resolved]} />)
  assert.equal(view.queryByText('工具返回成功'), null)
})

it('shows safe search links and a bounded fetch preview in existing tool cards', async () => {
  const url = 'https://example.com/article'
  const block = { type: 'tool_call' as const, phase: 'block_end' as const }
  const search = {
    ...block, tool_name: 'web_search', input: '{"query":"topic"}',
    output: { type: 'text' as const, text: JSON.stringify({ results: [
      { url, title: '搜索来源', snippet: '网页摘要' },
      { url: 'javascript:alert(1)', title: '非法来源' },
    ] }) },
  }
  const view = render(<ActivityBlock block={search} />)
  assert.ok(view.getByText('网页搜索'))
  const details = view.container.querySelector('details')!
  await act(async () => { details.open = true; fireEvent(details, new dom.Event('toggle') as unknown as Event) })
  assert.equal(view.getByRole('link', { name: '搜索来源' }).getAttribute('href'), url)
  assert.ok(view.getByText('网页摘要'))
  assert.equal(view.queryByRole('link', { name: '非法来源' }), null)
  view.rerender(<ActivityBlock block={{ ...search, output: { type: 'text', text: '{"results":[]}' } }} />)
  assert.ok(view.getByText('没有找到相关网页'))

  const content = '# 正文\n![外部图片](https://example.com/image.png)'
  view.rerender(<ActivityBlock block={{
    ...block, tool_name: 'web_fetch', input: JSON.stringify({ url }),
    output: { type: 'text', text: JSON.stringify({ url, title: '抓取页面', content, truncated: true }) },
  }} />)
  assert.ok(view.getByText('网页抓取'))
  assert.equal(view.getByRole('link', { name: '抓取页面' }).getAttribute('href'), url)
  assert.equal(view.getByLabelText('output 代码').textContent, content)
  assert.ok(view.getByText('仅显示正文开头，内容已截断'))
  assert.equal(view.container.querySelector('img'), null)
})
