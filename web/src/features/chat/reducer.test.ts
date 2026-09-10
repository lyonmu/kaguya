/// <reference types="node" />
import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { applyFrame } from './reducer'
import type { Block, ChatFrame, Turn } from './types'

const initial: Turn = { turn_index: 1, user_content: 'hi', model_id: '', model_name: '', api_protocol: '', started_at: new Date().toISOString(), duration_ms: 0, tool_calls: 0, blocks: [], status: 'streaming' }
const event = (block: Block): ChatFrame => ({ chat: { id: '1', flag: 'delta', block, content: '不要重复消费' }, model_id: '', model_name: '', api_protocol: '', created: 0, usage: { input_tokens: 1, output_tokens: 2, total_tokens: 3, cached_tokens: 0, reasoning_tokens: 0 } })

describe('chat frame reducer', () => {
  it('appends deltas without duplicating compatibility text or block_end text', () => {
    let turn = applyFrame(initial, event({ type: 'text', phase: 'start' }))
    turn = applyFrame(turn, event({ type: 'text', phase: 'delta', text: '你好' }))
    turn = applyFrame(turn, event({ type: 'text', phase: 'block_end', text: '你好' }))
    assert.equal(turn.blocks[0].text, '你好')
    assert.equal(initial.blocks.length, 0)
  })
  it('keeps reasoning and subsequent text blocks separate', () => {
    let turn = applyFrame(initial, event({ type: 'reasoning', phase: 'delta', text: '想一想' }))
    turn = applyFrame(turn, event({ type: 'text', phase: 'start' }))
    turn = applyFrame(turn, event({ type: 'text', phase: 'delta', text: '结论' }))
    assert.deepEqual(turn.blocks.map(block => block.text), ['想一想', '结论'])
  })
  it('merges parallel tool results by ID and replaces final input', () => {
    let turn = initial
    for (const id of ['a', 'b']) turn = applyFrame(turn, event({ type: 'tool_call', tool_call_id: id, phase: 'delta', input: '{"x":' }))
    turn = applyFrame(turn, event({ type: 'tool_call', tool_call_id: 'a', phase: 'block_end', input: '{"x":1}' }))
    turn = applyFrame(turn, event({ type: 'tool_result', tool_call_id: 'b', output: { type: 'error', text: '失败' }, is_error: true }))
    turn = applyFrame(turn, event({ type: 'tool_result', tool_call_id: 'a', output: { type: 'text', text: '成功' } }))
    assert.equal(turn.blocks.length, 2)
    assert.equal(turn.blocks[0].input, '{"x":1}')
    assert.equal(turn.blocks[0].output?.text, '成功')
    assert.equal(turn.blocks[1].is_error, true)
    assert.equal(turn.tool_calls, 2)
  })
  it('done only applies usage, preserving existing blocks', () => {
    const turn = applyFrame(initial, event({ type: 'text', phase: 'delta', text: 'hello' }))
    const done = event({ type: 'text', text: 'ignored' })
    done.chat.flag = 'done'
    done.finish_reason = 'step_limit'
    const result = applyFrame(turn, done)
    assert.equal(result.status, 'done')
    assert.equal(result.finish_reason, 'step_limit')
    assert.equal(result.usage?.total_tokens, 3)
    assert.deepEqual(result.blocks, turn.blocks)
  })
})
