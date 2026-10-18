/// <reference types="node" />
import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { mergeTitle, titleFromEvent, titleFromSnapshot } from './titleMerge'
import { conversationGuard, titleEventGuard, titleSnapshotsGuard } from './guards'

const entity = { id: 'c', title: 'manual', title_revision: 4, title_job_status: 'running' as const, title_job_revision: 2 }

describe('independent title and job revisions', () => {
  it('higher title/lower job and lower title/higher job merge independently', () => {
    const first = mergeTitle(entity, { id: 'c', title: 'new', revision: 5, job_status: 'pending', job_revision: 1 })
    assert.equal(first.value.title, 'new'); assert.equal(first.value.title_job_status, 'running')
    const second = mergeTitle(first.value, { id: 'c', title: 'old', revision: 3, job_status: 'failed', job_revision: 3 })
    assert.equal(second.value.title, 'new'); assert.equal(second.value.title_job_status, 'failed')
  })
  it('does not regress to unversioned responses or update other IDs', () => {
    assert.equal(mergeTitle(entity, { id: 'c', title: 'legacy', job_status: 'pending' }).value, entity)
    assert.equal(mergeTitle(entity, { id: 'other', title: 'bad', revision: 10 }).value, entity)
    assert.equal(mergeTitle({ id: 'c', title: 'legacy' }, { id: 'c', title: 'first', revision: 0 }).value.title_revision, 0)
  })
  it('equal revision is idempotent; conflicting fields retain confirmed data', () => {
    assert.equal(mergeTitle(entity, { id: 'c', title: 'manual', revision: 4 }).value, entity)
    const result = mergeTitle(entity, { id: 'c', title: 'conflict', revision: 4, job_status: 'failed', job_revision: 2 })
    assert.equal(result.conflict, true); assert.equal(result.value, entity)
  })
  it('events before/after snapshots converge without version coupling', () => {
    const snapshot = titleFromSnapshot({ id: 'c', title: 'manual', title_revision: 4, job_status: 'failed', job_revision: 3 })
    const event = titleFromEvent({ type: 'conversation.title.updated', conversation_id: 'c', title: 'renamed', revision: 5 })
    assert.deepEqual(mergeTitle(mergeTitle(entity, event).value, snapshot).value, mergeTitle(mergeTitle(entity, snapshot).value, event).value)
    const job = titleFromEvent({ type: 'conversation.title.job.updated', conversation_id: 'c', revision: 0, job_status: 'canceled', job_revision: 4 })
    assert.equal(job.title, undefined)
    assert.equal(mergeTitle(entity, job).value.title, 'manual')
  })
})

describe('title protocol guards', () => {
  it('accepts both events including job revision zero and legacy terminal title events', () => {
    assert.equal(titleEventGuard({ type: 'conversation.title.job.updated', conversation_id: 'c', job_status: 'pending', job_revision: 0 }).ok, true)
    assert.equal(titleEventGuard({ type: 'conversation.title.updated', conversation_id: 'c', revision: 0, status: 'failed' }).ok, true)
  })
  it('rejects fractional/negative/unsafe versions, incomplete jobs and unknown states', () => {
    for (const revision of [-1, 0.5, NaN, Number.MAX_SAFE_INTEGER + 1]) {
      assert.equal(titleEventGuard({ type: 'conversation.title.updated', conversation_id: 'c', revision }).ok, false)
      assert.equal(conversationGuard({ id: 'c', title: 'x', title_job_revision: revision }).ok, false)
    }
    for (const payload of [{ job_status: 'unknown', job_revision: 1 }, { job_status: 'pending' }, { job_revision: 1 }]) {
      assert.equal(titleEventGuard({ type: 'conversation.title.job.updated', conversation_id: 'c', ...payload }).ok, false)
    }
    assert.equal(titleEventGuard({ type: 'conversation.title.updated', conversation_id: '', revision: 0 }).ok, false)
  })
  it('requires complete, unique request coverage and null/zero for absent jobs', () => {
    assert.equal(conversationGuard({ id: 'c', title: 'x', title_job_revision: 0 }).ok, true)
    assert.equal(conversationGuard({ id: 'c', title: 'x', title_job_revision: 1 }).ok, false)
    assert.equal(conversationGuard({ id: 'c', title: 'x', title_job_status: null, title_job_revision: 1 }).ok, false)
    const item = { id: 'c', title: 'x', title_revision: 0, job_status: null, job_revision: 0 }
    const guard = titleSnapshotsGuard(['c', 'd'])
    assert.equal(guard({ items: [item], missing_ids: ['d'] }).ok, true)
    for (const value of [
      { items: [item], missing_ids: [] }, { items: [item], missing_ids: ['c', 'd'] },
      { items: [item, item], missing_ids: ['d'] }, { items: [{ ...item, id: 'x' }], missing_ids: ['d'] },
      { items: [{ ...item, job_revision: 1 }], missing_ids: ['d'] },
    ]) assert.equal(guard(value).ok, false)
  })
})
