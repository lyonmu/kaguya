import { ApiRequestError } from './errors'

/** wire字节上限包含字段、comment及行结束符，不以JS字符数代替。 */
export const MAX_FRAME_BYTES = 64 * 1024 * 1024
const newline = new Uint8Array([10])
const sizeFailure = () => new ApiRequestError('事件帧超过大小上限', { kind: 'protocol' })
const validationChunkBytes = 64 * 1024

// 连续缓冲避免每byte一个chunk/data行对象；扩容摊销线性，事件完成后释放容量。
class ByteBuffer {
  private bytes = new Uint8Array(0)
  private length = 0
  private readonly limit: number
  constructor(limit: number) { this.limit = limit }
  append(value: Uint8Array) {
    const required = this.length + value.length
    if (required > this.limit) throw sizeFailure()
    if (required > this.bytes.length) {
      const next = new Uint8Array(Math.min(this.limit, Math.max(256, this.bytes.length * 2, required)))
      next.set(this.view())
      this.bytes = next
    }
    this.bytes.set(value, this.length)
    this.length = required
  }
  view() { return this.bytes.subarray(0, this.length) }
  reset() { this.length = 0 }
  clear() { this.bytes = new Uint8Array(0); this.length = 0 }
}

/** 通用SSE framing；小limit仅供同算法边界fixture，生产使用默认64MiB。 */
export class EventStreamParser {
  private readonly onData: (raw: string) => void
  private readonly limit: number
  private readonly line: ByteBuffer
  private readonly data: ByteBuffer
  private decoder = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true })
  private wireBytes = 0
  private lineBytes = 0
  private hasData = false
  private pendingCR = false
  private firstLine = true
  private inspected = 0

  constructor(onData: (raw: string) => void, limit = MAX_FRAME_BYTES) {
    if (!Number.isSafeInteger(limit) || limit < 1 || limit > MAX_FRAME_BYTES) throw new RangeError('invalid SSE byte limit')
    this.onData = onData
    this.limit = limit
    this.line = new ByteBuffer(limit)
    this.data = new ByteBuffer(limit)
  }

  /** 原始输入只前进；可用于核对分片与整chunk扫描同样线性。 */
  get inspectedBytes() { return this.inspected }

  private charge() {
    this.inspected++
    if (++this.wireBytes > this.limit) throw sizeFailure()
  }

  private finishLine() {
    let bytes = this.line.view()
    if (this.firstLine && bytes.length >= 3 && bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf) bytes = bytes.subarray(3)
    this.firstLine = false
    try {
      // 校验不需要整行字符串；大UTF-8行会生成一个随后丢弃的巨大字符串。
      // streaming保留跨64KiB片段的code point，flush仍拒绝行尾半个字符。
      for (let offset = 0; offset < bytes.length; offset += validationChunkBytes) {
        this.decoder.decode(bytes.subarray(offset, offset + validationChunkBytes), { stream: true })
      }
      this.decoder.decode()
    } catch { throw new ApiRequestError('事件流包含无效UTF-8', { kind: 'protocol' }) }
    if (!bytes.length) {
      const raw = this.hasData ? this.decoder.decode(this.data.view()) : undefined
      this.wireBytes = 0
      this.hasData = false
      this.lineBytes = 0
      this.line.clear()
      this.data.clear()
      if (raw !== undefined) this.onData(raw)
      return
    }
    if (bytes.length >= 4 && bytes[0] === 100 && bytes[1] === 97 && bytes[2] === 116 && bytes[3] === 97 && (bytes.length === 4 || bytes[4] === 58)) {
      let start = bytes.length === 4 ? 4 : 5
      if (bytes[start] === 32) start++
      if (this.hasData) this.data.append(newline)
      this.data.append(bytes.subarray(start))
      this.hasData = true
    }
    this.lineBytes = 0
    this.line.reset()
  }

  push(chunk: Uint8Array, shouldStop: () => boolean = () => false) {
    let start = 0
    for (let offset = 0; offset < chunk.length; offset++) {
      if (shouldStop()) return
      const byte = chunk[offset]
      if (this.pendingCR) {
        this.pendingCR = false
        if (byte === 10) {
          this.charge() // CRLF的LF仍属于结束中的同一个事件。
          this.finishLine()
          start = offset + 1
          continue
        }
        this.finishLine()
        if (shouldStop()) return
      }
      this.charge()
      if (byte === 13 || byte === 10) {
        this.line.append(chunk.subarray(start, offset))
        start = offset + 1
        if (byte === 13) this.pendingCR = true
        else this.finishLine()
      } else if (++this.lineBytes > this.limit) throw sizeFailure()
    }
    // 在全部wire预算检查后才复制当前行片段，巨大未换行输入不会先decode。
    this.line.append(chunk.subarray(start))
  }

  finish(shouldStop: () => boolean = () => false) {
    // 末尾CR是真实行结束；没有空行结束的data/event仍丢弃。
    if (this.pendingCR && !shouldStop()) { this.pendingCR = false; this.finishLine() }
    this.clear()
  }

  clear() {
    this.line.clear()
    this.data.clear()
    this.wireBytes = 0
    this.lineBytes = 0
    this.hasData = false
    this.pendingCR = false
    // fatal streaming解码失败后不得将半个字符状态带到复用的parser。
    this.decoder = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true })
  }
}
