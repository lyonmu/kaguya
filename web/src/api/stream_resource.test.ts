/// <reference types="node" />
import { it } from 'node:test'
import assert from 'node:assert/strict'
import { ApiRequestError } from './errors'
import { readEventStream } from './stream'
import { MAX_FRAME_BYTES } from './streamParser'

// 显式发布资源验收；默认全量不把未运行的三轮负载误记为PASS。
// 使用真实reader和生产64MiB上限，按需生成64KiB片段，不预先分配整帧。
const enabled = process.env.KAGUYA_SSE_RESOURCE === '1'
const encode = (value: string) => new TextEncoder().encode(value)
const chunk = new Uint8Array(64 * 1024).fill(120)
const modes = ['complete', 'comment_complete', 'data_overflow', 'comment_overflow', 'abort', 'callback_failure', 'read_failure'] as const
type Mode = typeof modes[number]
type Memory = { rss: number; heapUsed: number; external: number }

function memory(): Memory {
  const { rss, heapUsed, external } = process.memoryUsage()
  return { rss, heapUsed, external }
}

function collect() {
  const bun = (globalThis as typeof globalThis & { Bun?: { gc(force: boolean): void } }).Bun
  assert.equal(typeof bun?.gc, 'function', 'resource acceptance requires the project Bun runtime')
  bun!.gc(true)
}

async function resourceCase(mode: Mode) {
  collect()
  const before = memory()
  const observed = { ...before }
  const observe = () => {
    const next = memory()
    for (const key of ['rss', 'heapUsed', 'external'] as const) observed[key] = Math.max(observed[key], next[key])
  }
  const signal = new AbortController()
  const prefix = mode.startsWith('comment_') ? encode(':') : encode('data: 🌙')
  const completed = mode === 'complete' || mode === 'callback_failure'
  const overflow = mode === 'data_overflow' || mode === 'comment_overflow'
  const limit = MAX_FRAME_BYTES + (overflow ? 1 : 0)
  const textEnd = limit - (completed || mode === 'comment_complete' ? 2 : 0)
  let sent = 0
  let pulls = 0
  let cancels = 0
  let reached!: () => void
  const inputComplete = new Promise<void>(resolve => { reached = resolve })
  const callbackError = new ApiRequestError('safe resource callback failure', { kind: 'business' })
  const body = new ReadableStream<Uint8Array>({
    pull(controller) {
      pulls++
      observe()
      if (sent === limit) {
        reached()
        if (mode === 'read_failure') controller.error(new Error('fake-secret-resource-read'))
        if (mode === 'comment_complete') controller.close()
        // 未关闭的流保持真实pending reader，abort/error/shouldStop负责终止。
        return
      }
      let part: Uint8Array
      if (sent < prefix.length) {
        // 把月亮的四个UTF-8字节分在多个chunk，不以字符数代替wire预算。
        part = prefix.subarray(sent, sent + 1)
      } else if (sent < textEnd) {
        part = chunk.subarray(0, Math.min(chunk.length, textEnd - sent))
      } else {
        part = encode('\n').subarray(0, 1)
      }
      sent += part.length
      controller.enqueue(part)
    },
    cancel() {
      cancels++
      observe()
      if (mode === 'abort' || mode === 'callback_failure') throw new Error('fake-secret-resource-close')
    },
  }, { highWaterMark: 0 })
  let events = 0
  const started = performance.now()
  const reading = readEventStream(body, raw => {
    events++
    // 整个payload只在同步callback内检查，结束后不留副本或frame数组。
    assert.equal(raw.length, MAX_FRAME_BYTES - prefix.length - 2 + 2)
    assert.equal(raw.slice(0, 2), '🌙')
    assert.equal(raw.slice(-1), 'x')
    observe()
    if (mode === 'callback_failure') throw callbackError
  }, { signal: signal.signal, shouldStop: () => completed && events === 1 })
  // 先登记rejection处理，避免达到上限时发生未处理的异步错误。
  const outcome = reading.then(() => ({ error: undefined as unknown }), error => ({ error: error as unknown }))
  if (mode === 'abort') {
    await inputComplete
    signal.abort()
  }
  const { error } = await outcome
  observe()
  switch (mode) {
    case 'complete':
    case 'comment_complete':
      assert.equal(error, undefined)
      break
    case 'data_overflow':
    case 'comment_overflow':
      assert.ok(error instanceof ApiRequestError && error.kind === 'protocol')
      assert.equal(error.message, '事件帧超过大小上限')
      break
    case 'abort':
      assert.ok(error instanceof DOMException && error.name === 'AbortError')
      break
    case 'callback_failure':
      assert.equal(error, callbackError)
      break
    case 'read_failure':
      assert.ok(error instanceof ApiRequestError && error.kind === 'network')
      assert.equal(error.message, '事件流读取失败')
      break
  }
  assert.equal(sent, limit)
  assert.equal(events, completed ? 1 : 0)
  assert.equal(cancels, mode === 'read_failure' || mode === 'comment_complete' ? 0 : 1)
  assert.equal(body.locked, false)
  assert.ok(pulls >= 1024, 'must exercise fragmented input rather than a single preallocated frame')
  assert.ok(observed.rss - before.rss <= 8 * MAX_FRAME_BYTES, 'sampled per-case RSS growth exceeds the fixed 512MiB envelope')
  return { mode, bytes: sent, chunks: pulls, events, cancels, locked: body.locked, elapsed_ms: performance.now() - started, before, observed }
}

it('accepts three real 64MiB SSE resource rounds and releases every terminal reader', { skip: !enabled, timeout: 120_000 }, async () => {
  collect()
  const baseline = memory()
  const settled: Memory[] = []
  let warmedRSS = 0
  const convergenceFailures: string[] = []
  for (let round = 1; round <= 3; round++) {
    for (const mode of modes) {
      const result = await resourceCase(mode)
      // resourceCase已经返回，parser、reader及callback局部引用不再由其栈持有。
      collect()
      const after = memory()
      assert.ok(after.heapUsed - baseline.heapUsed <= MAX_FRAME_BYTES / 2, 'retained heap exceeds the fixed 32MiB end-of-case envelope')
      assert.ok(after.external - baseline.external <= MAX_FRAME_BYTES / 2, 'retained external buffers exceed the fixed 32MiB end-of-case envelope')
      settled.push(after)
      console.info('SSE_RESOURCE ' + JSON.stringify({ round, ...result, after, baseline }))
    }
    // JSC/系统allocator可保留已释放的RSS；检查预热后两轮的收敛，不能声称RSS归零。
    if (round === 1) warmedRSS = settled.at(-1)!.rss
    else if (settled.at(-1)!.rss - warmedRSS > MAX_FRAME_BYTES / 2) {
      // 保留FAIL，先完整执行下一独立轮；不能用部分结果冒充三轮验收。
      convergenceFailures.push(`round ${round}: post-warmup RSS growth ${settled.at(-1)!.rss - warmedRSS} exceeds 32MiB`)
    }
  }
  assert.equal(settled.length, 21)
  assert.deepEqual(convergenceFailures, [], 'post-warmup RSS continues growing beyond the fixed 32MiB envelope')
})
