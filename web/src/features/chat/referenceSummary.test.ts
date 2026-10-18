/// <reference types="node" />
import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { referenceSelections, referenceSummaryGuard } from './referenceSummary'
import { sseFrameGuard, turnPageGuard } from './guards'
import { applyFrame } from './reducer'

const file = { path: 'pkg/a.go', included: true, status: 'included', hash: 'a'.repeat(64), ranges: [{ start: 1, end: 2 }], next_line: 3 }
const source = { type: 'directory', path: 'pkg', included: true, hash: 'b'.repeat(16), files: [file] }
const resolved = { preparation_state: 'resolved', sources: [source], budget_known: false, inline_bytes: 42, estimated_tokens: 12, partial_reason: 'context_budget' }

describe('reference summary boundaries', () => {
  it('keeps committed legacy hashes and ranges without inventing included proof', () => {
    const legacy = { sources: [{ type: 'file', path: 'a.go', hash: 'b'.repeat(16), ranges: [{ start: 1, end: 2 }] }], estimated_tokens: 12 }
    const checked = referenceSummaryGuard(legacy)
    assert.ok(checked.ok)
    assert.equal(checked.value.preparation_state, undefined)
    assert.equal(checked.value.sources[0].included, undefined)
    assert.equal(checked.value.sources[0].files.length, 0)
    assert.deepEqual(checked.value.sources[0].ranges, [{ start: 1, end: 2 }])
    assert.equal(referenceSummaryGuard(checked.value).ok, true)
    assert.equal(referenceSummaryGuard({ ...legacy, preparation_state: 'resolved' }).ok, false)
  })
  it('normalizes nullable arrays without turning unknown budget into known', () => {
    const checked = referenceSummaryGuard({ sources: null, warnings: null, continuation: null })
    assert.ok(checked.ok)
    assert.deepEqual(checked.value.sources, [])
    assert.deepEqual(checked.value.warnings, [])
    assert.deepEqual(checked.value.continuation, [])
    assert.equal(checked.value.budget_known, undefined)
    const known = referenceSummaryGuard(resolved)
    assert.ok(known.ok)
    assert.equal(known.value.budget_known, false)
    assert.deepEqual(known.value.sources[0].files[0].ranges, [{ start: 1, end: 2 }])
  })
  it('rejects fake inclusion, unknown states, invalid counts and ranges', () => {
    for (const changed of [
      { preparation_state: 'complete' }, { partial_reason: 'success' }, { inline_bytes: -1 }, { estimated_tokens: NaN }, { budget_known: 'true' },
      { sources: [{ ...source, files: [{ ...file, ranges: [{ start: 0, end: 2 }] }] }] },
      { sources: [{ ...source, files: [{ ...file, ranges: [{ start: 3, end: 2 }] }] }] },
      { sources: [{ ...source, files: [{ ...file, next_line: 2 }] }] },
      { sources: [{ ...source, files: [{ ...file, hash: 'b'.repeat(16) }] }] },
      { sources: [{ ...source, files: [{ ...file, status: 'future' }] }] },
      { sources: [{ ...source, included: false }] },
      { sources: [{ ...source, files: [{ ...file, included: false }] }] },
    ]) assert.equal(referenceSummaryGuard({ ...resolved, ...changed }).ok, false)
  })
  it('separates requested intent from read proof and only restores typed selections', () => {
    const checked = referenceSummaryGuard({ preparation_state: 'requested', sources: [{ type: 'directory', path: 'pkg', files: null }, { type: 'directory', path: 'pkg' }, { type: 'file', path: 'pkg/a.go' }] })
    assert.ok(checked.ok)
    assert.deepEqual(referenceSelections(checked.value), [{ type: 'directory', path: 'pkg' }, { type: 'file', path: 'pkg/a.go' }])
    for (const extra of [{ budget_known: false }, { sources: [source] }, { inline_bytes: 1 }, { scan_incomplete: true }]) {
      assert.equal(referenceSummaryGuard({ ...checked.value, ...extra }).ok, false)
    }
  })
  it('uses the same normalized summary for done and persisted history', () => {
    const frame = sseFrameGuard({ chat: { id: '123', flag: 'done' }, reference_summary: resolved })
    assert.ok(frame.ok)
    const turn = { turn_index: 1, user_content: 'inspect', started_at: '2026-10-06T00:00:00Z', blocks: [], model_name: '', model_id: '', api_protocol: '', duration_ms: 0, tool_calls: 0 }
    const history = turnPageGuard({ items: [{ ...turn, reference_summary: resolved }], page: 1, total_pages: 1 })
    assert.ok(history.ok)
    assert.deepEqual(applyFrame(turn, frame.value).reference_summary, history.value.items[0].reference_summary)
    assert.equal(turnPageGuard({ items: [{ ...turn, reference_summary: { ...resolved, preparation_state: 'future' } }], page: 1, total_pages: 1 }).ok, false)
  })
})
