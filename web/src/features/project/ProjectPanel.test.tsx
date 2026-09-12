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
  getComputedStyle: dom.getComputedStyle.bind(dom), IS_REACT_ACT_ENVIRONMENT: true,
}
const previous = new Map(Object.keys(globals).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
for (const [key, value] of Object.entries(globals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
const { render, cleanup, waitFor, act, fireEvent } = await import('@testing-library/react')
const { App } = await import('antd')
const { ProjectPanel } = await import('./ProjectPanel')
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
it('shows multiple project folders with nested conversations and active highlighting', async () => {
  const projects = [
    { id: 'p1', name: 'kaguya', path: '/root/kaguya', description: '', created_at: '' },
    { id: 'p2', name: 'blog', path: '/root/blog', description: '', created_at: '' },
  ]
  let selected = ''
  globalThis.fetch = (async url => {
    const parsed = new URL(String(url), 'http://localhost')
    if (parsed.pathname.endsWith('/project/page')) return response({ items: projects, total: 2 })
    const id = parsed.searchParams.get('project_id')
    return response({ items: id === 'p1' ? [{ id: 'c1', title: '完善中英文项目文档' }] : [{ id: 'c2', title: '增加文章标题和标签' }, { id: 'c3', title: '重构优化并删除 galleries' }], total: id === 'p1' ? 1 : 2 })
  }) as typeof fetch
  const view = render(<App><ProjectPanel disabled={false} activeId="c1" selected={projects[0]} onSelect={() => {}} onConversationSelect={(project, id) => { selected = `${project.id}:${id}` }} /></App>)
  await waitFor(() => assert.ok(view.getByText('重构优化并删除 galleries')))
  // 会话行现在带前置图标，标题文本单独成行，高亮类在按钮上。
  const active = view.getByRole('button', { name: '完善中英文项目文档' })
  assert.ok(active.classList.contains('active'))
  assert.ok(view.getByRole('region', { name: '项目 kaguya' }).contains(active))
  assert.ok(view.getByRole('region', { name: '项目 blog' }).contains(view.getByRole('button', { name: '增加文章标题和标签' })))
  fireEvent.click(view.getByRole('button', { name: '增加文章标题和标签' }))
  assert.equal(selected, 'p2:c2')
  assert.equal(view.queryByText('/root/kaguya'), null)
})

it('creates a project by navigating server folders from home', async () => {
  let selected = ''
  const paths: string[] = []
  globalThis.fetch = (async (url, init) => {
    const parsed = new URL(String(url), 'http://localhost')
    if (parsed.pathname.endsWith('/directories')) {
      const path = parsed.searchParams.get('path') || ''
      paths.push(path)
      return response(path ? { home: '/root', path, parent: '/root', items: [] } : { home: '/root', path: '/root', parent: '', items: [{ name: 'code', path: '/root/code' }] })
    }
    if (init?.method === 'POST') {
      const data = JSON.parse(String(init.body))
      assert.deepEqual(data, { name: '项目', path: '/root/code', description: '' })
      return response({ ...data, id: 'p1', created_at: '' })
    }
    return response({ items: [], total: 0, page: 1, page_size: 20 })
  }) as typeof fetch
  const view = render(<App><ProjectPanel disabled={false} onSelect={project => { selected = project?.id ?? '' }} /></App>)
  fireEvent.click(view.getByText('新建项目'))
  await waitFor(() => assert.ok(view.getByText('使用当前目录')))
  assert.equal((view.getByLabelText('项目目录') as HTMLInputElement).readOnly, true)
  fireEvent.change(view.getByLabelText('项目名称'), { target: { value: '项目' } })
  fireEvent.click(view.getByRole('button', { name: /code/ }))
  await waitFor(() => assert.ok(view.getByText('/root/code')))
  assert.equal(view.queryByText('无子文件夹'), null)
  assert.equal((view.getByLabelText('项目目录') as HTMLInputElement).value, '/root/code')
  fireEvent.click(view.getByRole('button', { name: 'OK' }))
  await waitFor(() => assert.equal(selected, 'p1'))
  assert.deepEqual(paths, ['', '/root/code'])
})
