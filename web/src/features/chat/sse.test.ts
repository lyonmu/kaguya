/// <reference types="node" />
import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { ApiRequestError } from '../../api/errors'
import { consumeSSE } from './sse'
import type { ChatFrame } from './types'

function frame(flag: 'start' | 'delta' | 'done', content?: string) {
  return { code: 100000, data: { chat: { id: '123', flag, content } } }
}
function stream(text: string, bytewise = false) {
  const bytes = new TextEncoder().encode(text)
  return new ReadableStream<Uint8Array>({ start(controller) {
    if (bytewise) for (const byte of bytes) controller.enqueue(new Uint8Array([byte]))
    else controller.enqueue(bytes)
    controller.close()
  } })
}

describe('SSE transport', () => {
  it('handles split UTF-8, CRLF, comments and multiple events', async () => {
    const received: ChatFrame[] = []
    const text = ': heartbeat\r\n\r\n' + [frame('start'), frame('delta', '你好🌙'), frame('done')].map(value => `data: ${JSON.stringify(value)}\r\n\r\n`).join('')
    await consumeSSE(stream(text, true), value => received.push(value))
    assert.deepEqual(received.map(value => value.chat.flag), ['start', 'delta', 'done'])
    assert.equal(received[1].chat.content, '你好🌙')
  })
  it('joins multiline data and supports CR separators', async () => {
    const text = 'event: message\rdata: {"code":100000,\rdata: "data":{"chat":{"id":"1","flag":"done"}}}\r\r'
    let received = false
    await consumeSSE(stream(text, true), () => { received = true })
    assert.equal(received, true)
  })
  it('rejects API failures and malformed events', async () => {
    await assert.rejects(consumeSSE(stream('data: {"code":500,"message":"模型不可用"}\n\n'), () => {}), /模型不可用/)
    await assert.rejects(consumeSSE(stream('data: not-json\n\n'), () => {}), /无法解析/)
    await assert.rejects(consumeSSE(stream('data: {"code":100000}\n\n'), () => {}), /格式不正确/)
  })
  it('attaches the conversation id to a failing frame', async () => {
    await assert.rejects(
      consumeSSE(stream('data: {"code":102000,"message":"生成失败","data":{"chat":{"id":"abc","flag":"error"}}}\n\n'), () => {}),
      (error: unknown) => error instanceof ApiRequestError && error.kind === 'business' && error.context?.conversationId === 'abc',
    )
    // 首帧失败尚无会话 ID 时回退到请求已知 ID。
    await assert.rejects(
      consumeSSE(stream('data: {"code":102000,"message":"生成失败"}\n\n'), () => {}, { conversationId: 'known' }),
      (error: unknown) => error instanceof ApiRequestError && error.context?.conversationId === 'known',
    )
  })
  it('does not silently mark a disconnected stream complete', async () => {
    await assert.rejects(consumeSSE(stream(`data: ${JSON.stringify(frame('start'))}\n\n`), () => {}), /连接意外中断/)
  })
  it('stops at done even when the connection remains open', async () => {
    let cancelled = false
    const body = new ReadableStream<Uint8Array>({
      start(controller) { controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(frame('done'))}\n\n`)) },
      cancel() { cancelled = true },
    })
    await consumeSSE(body, () => {})
    assert.equal(cancelled, true)
  })
})

describe('SSE runtime validation', () => {
  const envelope = (data: unknown) => `data: ${JSON.stringify({ code: 100000, data })}\n\n`
  it('rejects wrong types and keeps accepting valid extra fields', async () => {
    // 非字符串 delta 内容必须拒绝，而不是进入 reducer。
    await assert.rejects(consumeSSE(stream(envelope({ chat: { id: '1', flag: 'delta', content: 42 } })), () => {}), /格式不正确/)
    // 缺失 chat 或未知 flag 同样拒绝。
    await assert.rejects(consumeSSE(stream(envelope({ chat: { id: '1', flag: 'unknown' } })), () => {}), /格式不正确/)
    await assert.rejects(consumeSSE(stream(envelope({ result: 1 })), () => {}), /格式不正确/)
    // usage 字段出现时必须是数字。
    await assert.rejects(consumeSSE(stream(envelope({ chat: { id: '1', flag: 'done' }, usage: { total_tokens: 'x' } })), () => {}), /格式不正确/)
    // 合法额外字段与可选字段继续可用（done 帧结束流）。
    const received: ChatFrame[] = []
    const valid = `data: ${JSON.stringify({ code: 100000, data: { chat: { id: '1', flag: 'delta', content: 'ok' }, usage: { input_tokens: 1, output_tokens: 2, total_tokens: 3 }, extra: { future: true } }, message: 'ok', extra: 1 })}\n\n`
    await consumeSSE(stream(valid + envelope({ chat: { id: '1', flag: 'done' }, usage: { total_tokens: 3 } })), value => received.push(value))
    assert.equal(received.length, 2)
    assert.equal(received[0].chat.content, 'ok')
  })
  it('accepts optional known block phases and rejects invalid phases', async () => {
    for (const phase of [undefined, 'start', 'delta', 'block_end']) {
      const block = phase === undefined ? { type: 'text', text: 'ok' } : { type: 'text', text: 'ok', phase }
      const received: ChatFrame[] = []
      await consumeSSE(stream(envelope({ chat: { id: '1', flag: 'delta', block } }) + envelope({ chat: { id: '1', flag: 'done' } })), value => received.push(value))
      assert.equal(received.length, 2)
    }
    for (const phase of [null, 'future', 1]) {
      await assert.rejects(consumeSSE(stream(envelope({ chat: { id: '1', flag: 'delta', block: { type: 'text', phase } } })), () => {}), /格式不正确/)
    }
  })
  it('rejects an error frame without a chat id only when the field is missing', async () => {
    await assert.rejects(consumeSSE(stream(envelope({ chat: { flag: 'error' } })), () => {}), /格式不正确/)
    // 会话尚未建立时后端允许空 id。
    await assert.rejects(consumeSSE(stream(envelope({ chat: { id: '', flag: 'error', content: '模型不可用' } })), () => {}), /模型不可用/)
  })
})

describe('approval frames', () => {
  const envelope = (data: unknown) => `data: ${JSON.stringify({ code: 100000, data })}\n\n`
  const approval = (status: string) => ({ chat: { id: '1', flag: 'approval' }, approval: { approval_id: 'a1', tool_call_id: 'c1', tool_name: 'write', effect: 'mutating', mode: 'full_approval', status, input: '{"path":"a"}' } })
  it('delivers approval frames without ending the turn', async () => {
    const received: ChatFrame[] = []
    await consumeSSE(stream(envelope(approval('pending')) + envelope(approval('approved')) + envelope({ chat: { id: '1', flag: 'done' } })), value => received.push(value))
    assert.deepEqual(received.map(value => value.chat.flag), ['approval', 'approval', 'done'])
    assert.equal(received[0].approval?.status, 'pending')
    assert.equal(received[1].approval?.approval_id, 'a1')
  })
  it('rejects approval frames without the required identity', async () => {
    await assert.rejects(consumeSSE(stream(envelope({ chat: { id: '1', flag: 'approval' }, approval: { status: 'pending' } })), () => {}), /格式不正确/)
    await assert.rejects(consumeSSE(stream(envelope({ chat: { id: '1', flag: 'approval' } })), () => {}), /格式不正确/)
  })
})

describe('failed reference audit frames', () => {
  const summary = { preparation_state: 'requested', sources: [{ type: 'directory', path: 'pkg' }] }
  const error = (id: string, reference_summary: unknown = summary, code = 102017) => `data: ${JSON.stringify({ code, message: 'reference unavailable', data: { chat: { id, flag: 'error' }, reference_summary } })}\n\n`
  it('delivers a validated audit before business failure without dispatching done', async () => {
    const frames: ChatFrame[] = [], audits: string[] = []
    await assert.rejects(consumeSSE(stream(error('123')), frame => frames.push(frame), {
      conversationId: '123', onErrorAudit: (id, value) => { audits.push(id); assert.equal(value.preparation_state, 'requested') },
    }), (reason: unknown) => reason instanceof ApiRequestError && reason.kind === 'business')
    assert.deepEqual(audits, ['123'])
    assert.deepEqual(frames, [])
  })
  it('rejects mismatched identity and malformed summaries before audit callbacks', async () => {
    let audits = 0
    for (const text of [error('other'), error('123', { ...summary, preparation_state: 'future' }), error(''), error('', summary, 100000)]) {
      await assert.rejects(consumeSSE(stream(text), () => { assert.fail('error must not become done') }, { conversationId: '123', onErrorAudit: () => { audits++ } }),
        (reason: unknown) => reason instanceof ApiRequestError && reason.kind === 'protocol')
    }
    assert.equal(audits, 0)
  })
  it('binds errors to the start frame of a newly created conversation', async () => {
    let audits = 0
    await assert.rejects(consumeSSE(stream(`data: ${JSON.stringify(frame('start'))}\n\n` + error('other')), () => {}, { onErrorAudit: () => { audits++ } }), /身份不匹配/)
    assert.equal(audits, 0)
  })
})
