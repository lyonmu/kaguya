/// <reference types="node" />
import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { mergeDetail, normalizedPermissionMode, upsertApproval } from './session'
import type { Approval, Conversation } from './types'

const conversation = (turnCount: number, title: string): Conversation => ({ is_project: false, id: 'c1', title, favorite: false, turn_count: turnCount, model_id: '', model_name: '', created_at: '', last_message_at: '', duration_ms: 0, tool_calls: 0, usage: { input_tokens: 0, output_tokens: 0, total_tokens: 0, cached_tokens: 0, cache_creation_tokens: 0, reasoning_tokens: 0 } })

describe('chat session merge', () => {
  it('does not regress turn count and keeps a generated title over a late default', () => {
    assert.equal(mergeDetail(conversation(3, '已生成标题'), conversation(2, '新对话')), null)
    const merged = mergeDetail(conversation(1, '已生成标题'), conversation(2, '新对话'))
    assert.equal(merged?.title, '已生成标题')
    assert.equal(merged?.turn_count, 2)
  })
  it('only accepts stable permission modes', () => {
    assert.equal(normalizedPermissionMode('auto_approval'), 'auto_approval')
    assert.equal(normalizedPermissionMode('future_mode'), undefined)
    assert.equal(normalizedPermissionMode(undefined), undefined)
  })
  it('keeps a resolved approval when a delayed pending frame arrives', () => {
    const pending: Approval = { approval_id: 'a1', tool_call_id: 'c1', tool_name: 'write', effect: 'mutating', mode: 'full_access', status: 'pending' }
    const approved: Approval = { ...pending, status: 'approved' }
    const current = upsertApproval(new Map(), approved)
    assert.equal(upsertApproval(current, pending).get('a1')?.status, 'approved')
    assert.equal(upsertApproval(current, { ...approved, status: 'consumed' }).get('a1')?.status, 'consumed')
  })
})

it('does not regress execution results when delayed approval decisions arrive', () => {
  const succeeded: Approval = { approval_id: 'a1', tool_call_id: 'c1', tool_name: 'write', effect: 'mutating', mode: 'full_approval', status: 'consumed', execution_state: 'succeeded' }
  const current = upsertApproval(new Map(), succeeded)
  assert.equal(upsertApproval(current, { ...succeeded, status: 'approved', execution_state: 'not_started' }), current)
  assert.equal(upsertApproval(current, { ...succeeded, execution_state: 'unknown' }), current)
  assert.equal(upsertApproval(current, { ...succeeded, execution_state: 'failed' }), current)
})
