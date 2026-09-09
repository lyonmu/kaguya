/// <reference types="node" />
import { afterEach, it } from 'node:test'
import assert from 'node:assert/strict'
import { deleteProject, fetchDirectories, fetchProject, fetchProjects, saveProject } from './api'
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
it('filters conversations and creates new chats in the selected project', async () => {
  globalThis.fetch = (async (url, init) => {
    if (init?.method === 'POST') {
      assert.equal(JSON.parse(String(init.body)).project_id, 'project-1')
      return new Response('data: {"code":100000,"data":{"chat":{"id":"123","flag":"done"}}}\n\n', { headers: { 'Content-Type': 'text/event-stream' } })
    }
    assert.equal(new URL(String(url), 'http://localhost').searchParams.get('project_id'), 'project-1')
    return Response.json({ code: 100000, data: { items: [], total: 0 } })
  }) as typeof fetch
  await fetchConversations('', false, 1, undefined, 'project-1')
  await streamChat('', 'hello', new AbortController().signal, () => {}, undefined, 'project-1')
})
