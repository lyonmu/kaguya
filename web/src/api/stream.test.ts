/// <reference types="node" />
import { it } from 'node:test'
import assert from 'node:assert/strict'
import { ApiRequestError } from './errors'
import { readEventStream } from './stream'
import { EventStreamParser, MAX_FRAME_BYTES } from './streamParser'

const encode = (value: string) => new TextEncoder().encode(value)
const protocol = (error: unknown) => error instanceof ApiRequestError && error.kind === 'protocol'
function parse(text: string, limit: number, bytewise = false) {
  const events: string[] = []
  const parser = new EventStreamParser(raw => events.push(raw), limit)
  const bytes = encode(text)
  if (bytewise) for (let i = 0; i < bytes.length; i++) parser.push(bytes.subarray(i, i + 1))
  else parser.push(bytes)
  parser.finish()
  return { events, inspected: parser.inspectedBytes }
}
function source(chunks: Uint8Array[], options: { open?: boolean; cancelError?: Error } = {}) {
  let canceled = 0
  const body = new ReadableStream<Uint8Array>({
    start(controller) { for (const chunk of chunks) controller.enqueue(chunk); if (!options.open) controller.close() },
    cancel() { canceled++; if (options.cancelError) throw options.cancelError },
  })
  return { body, canceled: () => canceled }
}

it('enforces exact wire byte limits including fields, comments and blank terminators', () => {
  for (const text of ['data: x\n\n', ': note\ndata: x\n\n', 'event: msg\ndata: x\n\n', 'unknown: x\ndata: x\n\n', 'data: 🌙\n\n']) {
    const size = encode(text).length
    for (const split of [false, true]) {
      assert.deepEqual(parse(text, size, split).events, ['x'].map(value => text.includes('🌙') ? '🌙' : value))
      assert.throws(() => parse(text, size - 1, split), protocol)
    }
  }
})

it('bounds unfinished data and comment lines and comment-only events without blank lines', () => {
  for (const text of ['data: ' + 'x'.repeat(60), ':' + 'x'.repeat(60), ': x\n'.repeat(20)]) {
    for (const split of [false, true]) assert.throws(() => parse(text, 32, split), protocol)
  }
  const heartbeat = ': x\n\n'
  assert.deepEqual(parse(heartbeat.repeat(1000), encode(heartbeat).length).events, [])
})

it('handles CRLF, CRCR, LF, multiline data and empty data fields across every byte boundary', () => {
  for (const ending of ['\r\n', '\r', '\n']) {
    const text = `data: first${ending}data: second${ending}${ending}data${ending}data:${ending}data: ${ending}${ending}`
    for (const split of [false, true]) assert.deepEqual(parse(text, 128, split).events, ['first\nsecond', '\n\n'])
  }
  // CRLF最后一个LF仍要计入旧事件，不能提前dispatch绕过1byte超限。
  const text = 'data:x\r\n\r\n'
  assert.deepEqual(parse(text, encode(text).length, true).events, ['x'])
  assert.throws(() => parse(text, encode(text).length - 1, true), protocol)
})

it('discards unterminated EOF events and dispatches only actually terminated CR frames', () => {
  for (const text of ['data: partial', 'data: partial\n', 'data: partial\r', 'data: complete\n\ndata: partial\n']) {
    assert.deepEqual(parse(text, 64, true).events, text.includes('complete') ? ['complete'] : [])
  }
  assert.deepEqual(parse('data:x\r\r', 8, true).events, ['x'])
})

it('validates complete UTF-8 lines and strips BOM only at the start of the connection', () => {
  const text = '\uFEFFdata: 你好🌙\n\ndata: \uFEFFvalue\n\n'
  assert.deepEqual(parse(text, 128, true).events, ['你好🌙', '\uFEFFvalue'])
  for (const suffix of [[0xff], [0xc0, 0xaf], [0xe2, 0x82]]) {
    for (const prefix of ['data: ', ':', 'other: ']) {
      const bytes = new Uint8Array([...encode(prefix), ...suffix, 10, 10])
      const parser = new EventStreamParser(() => assert.fail('invalid UTF-8 must not dispatch'), 128)
      assert.throws(() => { for (const byte of bytes) parser.push(new Uint8Array([byte])) }, error => protocol(error) && (error as Error).message === '事件流包含无效UTF-8')
    }
  }
})

it('inspects each wire byte once for long one-byte fragments and many fields', () => {
  const text = `data: ${'x'.repeat(4000)}\ndata: 你好🌙\n\n` + ': x\n\n'.repeat(1000)
  for (const split of [false, true]) {
    const result = parse(text, 8192, split)
    assert.equal(result.inspected, encode(text).length)
    assert.deepEqual(result.events, ['x'.repeat(4000) + '\n你好🌙'])
  }
})

it('validates UTF-8 across validation chunks and rejects incomplete code points at each line end', () => {
  const boundary = 64 * 1024
  const leading = 'x'.repeat(boundary - encode('data: ').length - 1)
  // 月亮的首字节位于第一个校验片段末尾，后续三个字节在下一个片段。
  const valid = `data: ${leading}🌙tail\n\n`
  assert.deepEqual(parse(valid, boundary * 2).events, [leading + '🌙tail'])
  for (const field of ['data: ', ':', 'unknown: ']) {
    const padding = new Uint8Array(boundary - encode(field).length - 1).fill(120)
    const bad = new Uint8Array([...encode(field), ...padding, 0xe2, 0x82, 10, 10])
    const events: string[] = []
    const parser = new EventStreamParser(raw => events.push(raw), boundary * 2)
    assert.throws(() => parser.push(bad), error => protocol(error) && (error as Error).message === '事件流包含无效UTF-8')
    assert.deepEqual(events, [])
    parser.clear()
    // clear必须清掉fatal解码器的streaming状态，普通下一帧保持可用。
    parser.push(encode('data: ok\n\n'))
    parser.finish()
    assert.deepEqual(events, ['ok'])
  }
})

it('validates a real 64MiB comment without allocating a whole discarded decoded line', { timeout: 30_000 }, async () => {
  const bytes = new Uint8Array(MAX_FRAME_BYTES).fill(120)
  bytes[0] = 58
  bytes[bytes.length - 2] = bytes[bytes.length - 1] = 10
  const original = globalThis.TextDecoder
  let largestDecode = 0
  globalThis.TextDecoder = class extends original {
    override decode(input?: AllowSharedBufferSource, options?: TextDecodeOptions) {
      largestDecode = Math.max(largestDecode, input?.byteLength ?? 0)
      return super.decode(input, options)
    }
  }
  try {
    const stream = source([bytes])
    await readEventStream(stream.body, () => assert.fail('comment must not dispatch'))
    assert.equal(stream.body.locked, false)
    assert.ok(largestDecode <= 64 * 1024, 'UTF-8 validation must not create a frame-sized discarded string')
  } finally { globalThis.TextDecoder = original }
})

it('does not inspect or dispatch bytes following shouldStop in the same chunk', async () => {
  let stopped = false
  const events: string[] = []
  const first = 'data: done\n\n'
  const parser = new EventStreamParser(raw => { events.push(raw); stopped = true }, 32)
  parser.push(encode(first + 'data: ' + 'x'.repeat(100)), () => stopped)
  assert.deepEqual(events, ['done'])
  assert.equal(parser.inspectedBytes, encode(first).length)
  const stream = source([encode(first + 'data: trailing\n\n')], { open: true })
  stopped = false
  let dispatched = 0
  await readEventStream(stream.body, () => { stopped = true; dispatched++ }, { shouldStop: () => stopped })
  assert.equal(dispatched, 1)
  assert.equal(stream.canceled(), 1)
  assert.equal(stream.body.locked, false)
})

it('rejects sustained wire beyond real 64MiB before decoding or copying an unfinished line', { timeout: 30_000 }, async () => {
  const bytes = new Uint8Array(MAX_FRAME_BYTES + 1).fill(120)
  bytes.set(encode('data: '))
  const original = globalThis.TextDecoder
  let decodes = 0
  globalThis.TextDecoder = class extends original {
    override decode(input?: AllowSharedBufferSource, options?: TextDecodeOptions) { decodes++; return super.decode(input, options) }
  }
  try {
    for (const open of [false, true]) {
      const stream = source([bytes], { open })
      await assert.rejects(readEventStream(stream.body, () => assert.fail('oversize must not dispatch')), protocol)
      assert.equal(decodes, 0)
      assert.equal(stream.canceled(), open ? 1 : 0)
      assert.equal(stream.body.locked, false)
    }
  } finally { globalThis.TextDecoder = original }
})

it('accepts a real 64MiB complete frame and large chunks containing multiple legal frames', { timeout: 30_000 }, async () => {
  const bytes = new Uint8Array(MAX_FRAME_BYTES).fill(120)
  bytes.set(encode('data: '))
  bytes[MAX_FRAME_BYTES - 2] = 10
  bytes[MAX_FRAME_BYTES - 1] = 10
  const stream = source([bytes])
  let dispatched = 0
  await readEventStream(stream.body, raw => { dispatched++; assert.equal(raw.length, MAX_FRAME_BYTES - 8) })
  assert.equal(dispatched, 1)
  assert.equal(stream.body.locked, false)
  // chunk总长超限，但两个独立comment事件各小于上限，不能整chunk拒绝。
  const large = new Uint8Array(MAX_FRAME_BYTES + 10).fill(120)
  const middle = Math.floor(large.length / 2)
  large[0] = large[middle] = 58
  large[middle - 2] = large[middle - 1] = large[large.length - 2] = large[large.length - 1] = 10
  const multiple = source([large])
  await readEventStream(multiple.body, () => assert.fail('comments do not dispatch'))
  assert.equal(multiple.body.locked, false)
})

it('keeps callback or protocol failure when cancel also fails and always releases the reader', async () => {
  const callbackError = new ApiRequestError('safe callback failure', { kind: 'business' })
  const callback = source([encode('data: x\n\n')], { open: true, cancelError: new Error('fake-secret-close') })
  await assert.rejects(readEventStream(callback.body, () => { throw callbackError }), error => error === callbackError)
  assert.equal(callback.canceled(), 1)
  assert.equal(callback.body.locked, false)
  const invalid = source([new Uint8Array([...encode('data: '), 0xff, 10, 10])], { open: true, cancelError: new Error('fake-secret-close') })
  await assert.rejects(readEventStream(invalid.body, () => {}), error => protocol(error) && !(error as Error).message.includes('fake-secret'))
  assert.equal(invalid.body.locked, false)
})

it('classifies read and cleanup failures without exposing upstream error content', async () => {
  const broken = new ReadableStream<Uint8Array>({ start(controller) { controller.error(new Error('fake-secret-read')) } })
  await assert.rejects(readEventStream(broken, () => {}), error => error instanceof ApiRequestError && error.kind === 'network' && error.message === '事件流读取失败')
  assert.equal(broken.locked, false)
  const cleanup = source([encode(': heartbeat\n\n')], { open: true, cancelError: new Error('fake-secret-close') })
  await assert.rejects(readEventStream(cleanup.body, () => {}, { shouldStop: () => true }), error => error instanceof ApiRequestError && error.kind === 'network' && error.message === '事件流关闭失败')
  assert.equal(cleanup.canceled(), 1)
  assert.equal(cleanup.body.locked, false)
})

it('cancels pre-aborted and pending readers once and preserves AbortError even on cleanup failure', async () => {
  for (const beforeRead of [false, true]) {
    const controller = new AbortController()
    const stream = source([], { open: true, cancelError: new Error('fake-secret-close') })
    if (beforeRead) controller.abort()
    const reading = readEventStream(stream.body, () => assert.fail('abort cannot dispatch'), { signal: controller.signal })
    if (!beforeRead) controller.abort()
    await assert.rejects(reading, error => error instanceof DOMException && error.name === 'AbortError')
    assert.equal(stream.canceled(), 1)
    assert.equal(stream.body.locked, false)
  }
})
