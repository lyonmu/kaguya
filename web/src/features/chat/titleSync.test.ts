/// <reference types="node" />
import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { TitleSync } from './titleSync'
import { mergeTitle } from './titleMerge'
import type { TitleSnapshots, TitleUpdate } from './types'

type Request = { ids: string[]; signal: AbortSignal; resolve: (value: TitleSnapshots) => void; reject: (error: Error) => void }
const tick = () => new Promise(resolve => setTimeout(resolve, 0))
const snapshot = (ids: string[], revision = 1): TitleSnapshots => ({ items: ids.map(id => ({ id, title: `title-${revision}`, title_revision: revision, job_status: 'succeeded', job_revision: revision })), missing_ids: [] })
function fixture() {
  const requests: Request[] = []
  const sync = new TitleSync((ids, signal) => new Promise((resolve, reject) => { requests.push({ ids, signal, resolve, reject }) }))
  const updates: TitleUpdate[] = [], missing: string[] = [], errors: string[] = []
  const scope = sync.register({ update: value => { updates.push(value); return false }, missing: id => missing.push(id), error: message => errors.push(message) })
  return { sync, scope, requests, updates, missing, errors }
}

describe('Provider title snapshot coordinator', () => {
  it('batches 801 IDs at 200, at most four inflight, and deduplicates scope IDs', async () => {
    const f = fixture(), ids = Array.from({ length: 801 }, (_, i) => `${i}`)
    f.scope.setIDs([...ids, ...ids])
    const second = f.sync.register({ update: () => false, missing: () => {}, error: () => {} })
    second.setIDs(ids)
    const connection = new AbortController(); f.sync.connect(connection.signal)
    assert.equal(f.requests.length, 4)
    assert.equal(f.requests.reduce((sum, request) => sum + request.ids.length, 0), 800)
    f.requests[0].resolve(snapshot(f.requests[0].ids)); await tick()
    assert.equal(f.requests.length, 5); assert.equal(f.requests[4].ids.length, 1)
    for (const request of f.requests.slice(1)) request.resolve(snapshot(request.ids))
    await tick()
    assert.equal(new Set(f.requests.flatMap(request => request.ids)).size, 801)
    assert.equal(f.updates.length, 801)
    connection.abort(); f.scope.close(); second.close()
  })
  it('starts a finite snapshot when a newly loaded middle page registers IDs', async () => {
    const f = fixture(); f.scope.setIDs(['head'])
    const connection = new AbortController(); f.sync.connect(connection.signal)
    f.requests[0].resolve(snapshot(['head'])); await tick()
    f.scope.setIDs(['head', 'middle', 'tail']); assert.equal(f.requests.length, 2)
    assert.deepEqual(f.requests[1].ids, ['middle', 'tail'])
    f.scope.setIDs(['head', 'middle', 'tail']); assert.equal(f.requests.length, 2)
    connection.abort()
  })
  it('old generation results/finally cannot apply or free slots in the next generation', async () => {
    const f = fixture(); f.scope.setIDs(Array.from({ length: 801 }, (_, i) => `${i}`))
    const first = new AbortController(); f.sync.connect(first.signal)
    const second = new AbortController(); f.sync.connect(second.signal)
    assert.equal(f.requests.length, 8)
    assert.ok(f.requests.slice(0, 4).every(request => request.signal.aborted))
    for (const request of f.requests.slice(0, 4)) request.resolve(snapshot(request.ids, 99))
    await tick(); assert.equal(f.updates.length, 0); assert.equal(f.requests.length, 8)
    f.requests[4].resolve(snapshot(f.requests[4].ids, 2)); await tick()
    assert.equal(f.requests.length, 9); assert.equal(f.updates.length, 200)
    second.abort()
  })
  it('unregistering one reference retains the request; the last reference cancels', () => {
    const f = fixture(); f.scope.setIDs(['c'])
    const other = f.sync.register({ update: () => false, missing: () => {}, error: () => {} }); other.setIDs(['c'])
    f.sync.connect(new AbortController().signal)
    f.scope.close(); assert.equal(f.requests[0].signal.aborted, false)
    other.close(); assert.equal(f.requests[0].signal.aborted, true)
  })
  it('old missing cannot remove a newly registered scope and only it gets a follow-up check', async () => {
    const f = fixture(); f.scope.setIDs(['c', 'keep'])
    f.sync.connect(new AbortController().signal)
    f.scope.setIDs(['keep']); f.scope.setIDs(['keep', 'c'])
    f.requests[0].resolve({ items: snapshot(['keep']).items, missing_ids: ['c'] }); await tick()
    assert.deepEqual(f.missing, []); assert.equal(f.requests.length, 2); assert.deepEqual(f.requests[1].ids, ['c'])
    f.requests[1].resolve(snapshot(['c'])); await tick()
    assert.equal(f.updates.at(-1)?.id, 'c'); f.scope.close()
  })
  it('an aborted last-scope request does not strand a later registration', async () => {
    const f = fixture(); f.scope.setIDs(['c']); f.sync.connect(new AbortController().signal)
    f.scope.setIDs([]); assert.equal(f.requests[0].signal.aborted, true)
    f.scope.setIDs(['c']); f.requests[0].resolve({ items: [], missing_ids: ['c'] }); await tick()
    assert.deepEqual(f.missing, []); assert.equal(f.requests.length, 2)
    f.requests[1].resolve(snapshot(['c'])); await tick(); assert.equal(f.updates.length, 1)
  })
  it('a same-revision conflict triggers one resync and one safe error, never a loop', async () => {
    const f = fixture(); f.scope.close()
    let current = { id: 'c', title: 'confirmed', title_revision: 1 }
    const scope = f.sync.register({ update: update => { const merged = mergeTitle(current, update); current = merged.value; return merged.conflict }, missing: () => {}, error: message => f.errors.push(message) })
    scope.setIDs(['c']); const connection = new AbortController(); f.sync.connect(connection.signal)
    f.requests[0].resolve(snapshot(['c'])); await tick()
    assert.equal(f.requests.length, 2)
    f.requests[1].resolve(snapshot(['c'])); await tick()
    for (let i = 0; i < 20; i++) f.sync.update({ id: 'c', title: 'conflicting', revision: 1 })
    await tick(); assert.equal(f.requests.length, 2); assert.equal(f.errors.length, 1); assert.equal(current.title, 'confirmed')
    connection.abort(); scope.close()
  })
  it('failed snapshots report once without immediate retries; reconnect retries current IDs', async () => {
    const f = fixture(); f.scope.setIDs(['c']); const connection = new AbortController(); f.sync.connect(connection.signal)
    f.requests[0].reject(new Error('fake-secret')); await tick()
    assert.equal(f.requests.length, 1); assert.equal(f.errors.length, 1); assert.ok(!f.errors[0].includes('fake-secret'))
    connection.abort(); f.sync.connect(new AbortController().signal); assert.equal(f.requests.length, 2)
    f.scope.close()
  })
  it('successful deletion/missing removes membership so late title never resurrects it', async () => {
    const f = fixture(); f.scope.setIDs(['c']); f.sync.connect(new AbortController().signal)
    f.sync.remove('c'); assert.deepEqual(f.missing, ['c'])
    f.sync.update({ id: 'c', title: 'late', revision: 99 })
    f.requests[0].resolve(snapshot(['c'], 100)); await tick()
    assert.deepEqual(f.updates, []); assert.deepEqual(f.missing, ['c'])
  })
})
