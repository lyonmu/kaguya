/// <reference types="node" />
import { afterEach, it } from 'node:test'
import assert from 'node:assert/strict'
import { deleteProject, fetchDirectories, fetchProject, fetchProjects, saveProject } from './api'
import { fetchProjectFiles } from './api'
import { projectFilesGuard } from './guards'
import { fetchConversations, streamChat } from '../chat/api'

const originalFetch = globalThis.fetch
afterEach(() => { globalThis.fetch = originalFetch })
it('uses project CRUD and host directory endpoints', async () => {
  const calls: { url: URL; init?: RequestInit }[] = []
  globalThis.fetch = (async (url, init) => {
    calls.push({ url: new URL(String(url), 'http://localhost'), init })
    return Response.json({ code: 100000, data: {} })
  }) as typeof fetch
  await fetchProjects('项目', 2)
  await fetchProject('p1')
  await fetchDirectories()
  await fetchDirectories('/root/code')
  const data = { name: '项目', path: '/root/code', description: '' }
  await saveProject('', data)
  await saveProject('p1', data)
  await deleteProject('p1')
  assert.equal(calls[0].url.searchParams.get('keyword'), '项目')
  assert.equal(calls[0].url.searchParams.get('page'), '2')
  assert.ok(calls[1].url.pathname.endsWith('/v1/project/p1'))
  assert.ok(calls[2].url.pathname.endsWith('/v1/project/directories'))
  assert.equal(calls[3].url.searchParams.get('path'), '/root/code')
  assert.equal(calls[4].init?.method, 'POST')
  assert.deepEqual(JSON.parse(String(calls[4].init?.body)), data)
  assert.equal(calls[5].init?.method, 'PUT')
  assert.equal(calls[6].init?.method, 'DELETE')
})

it('validates candidate pagination limits, status and legacy files-only responses', async () => {
  const valid = { files: ['a'], items: [{ type: 'file', path: 'a' }], truncated: true, next_cursor: 'next', scan_incomplete: true, cursor_reset: true }
  assert.equal(projectFilesGuard(valid).ok, true)
  assert.equal(projectFilesGuard({ files: [], truncated: false }).ok, true)
  for (const invalid of [
    { ...valid, scan_incomplete: 'true' }, { ...valid, cursor_reset: 1 },
    { ...valid, truncated: false }, { ...valid, next_cursor: 'x'.repeat(4097) },
    { ...valid, files: Array.from({ length: 51 }, () => 'a') },
    { ...valid, items: Array.from({ length: 51 }, () => ({ type: 'file', path: 'a' })) },
    { ...valid, items: [{ type: 'unknown', path: 'a' }] },
    { ...valid, files: [''] }, { ...valid, items: [{ type: 'file', path: 'x'.repeat(4097) }] },
  ]) assert.equal(projectFilesGuard(invalid).ok, false)
  globalThis.fetch = (async (url, init) => {
    const parsed = new URL(String(url), 'http://localhost')
    assert.ok(parsed.pathname.endsWith('/project/a%2Fb/files'))
    assert.equal(parsed.searchParams.get('query'), '中文')
    assert.equal(parsed.searchParams.get('cursor'), 'next')
    assert.equal(parsed.searchParams.get('include_directories'), 'true')
    assert.ok(init?.signal)
    return Response.json({ code: 100000, data: valid })
  }) as typeof fetch
  const response = await fetchProjectFiles('a/b', '中文', { includeDirectories: true, cursor: 'next', signal: new AbortController().signal })
  assert.equal(response.scan_incomplete, true)
  assert.equal(response.cursor_reset, true)
})
it('ordinary conversations explicitly exclude project conversations', async () => {
  globalThis.fetch = (async url => {
    const params = new URL(String(url), 'http://localhost').searchParams
    assert.equal(params.get('is_project'), 'false')
    assert.equal(params.has('project_id'), false)
    return Response.json({ code: 100000, data: { items: [], total: 0 } })
  }) as typeof fetch
  await fetchConversations('', false, 1)
})
it('filters conversations and creates new chats in the selected project', async () => {
  globalThis.fetch = (async (url, init) => {
    if (init?.method === 'POST') {
      assert.equal(JSON.parse(String(init.body)).project_id, 'project-1')
      return new Response('data: {"code":100000,"data":{"chat":{"id":"123","flag":"done"}}}\n\n', { headers: { 'Content-Type': 'text/event-stream' } })
    }
    assert.equal(new URL(String(url), 'http://localhost').searchParams.get('project_id'), 'project-1')
    assert.equal(new URL(String(url), 'http://localhost').searchParams.get('is_project'), 'true')
    return Response.json({ code: 100000, data: { items: [], total: 0 } })
  }) as typeof fetch
  await fetchConversations('', false, 1, undefined, 'project-1')
  await streamChat('', 'hello', new AbortController().signal, () => {}, undefined, 'project-1')
})
