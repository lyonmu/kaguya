import { SUCCESS_CODE } from '../../api/codes'
import { ApiRequestError } from '../../api/errors'
import { readEventStream } from '../../api/stream'
import { isApiEnvelope, isRecord, isString } from '../../api/transport'
import { sseFrameGuard } from './guards'
import type { ChatFrame, ReferenceSummary } from './types'

export interface ConversationSSEOptions {
  /** 首帧前已知的会话 ID，用于把失败关联到当前会话。 */
  conversationId?: string
  onErrorAudit?: (conversationId: string, summary: ReferenceSummary) => void
}

// 失败帧尽量从 payload.data.chat.id 取出会话 ID；首帧失败时回退请求已知 ID。
const frameConversationId = (payload: unknown): string | undefined => {
  if (!isRecord(payload) || !isRecord(payload.data)) return undefined
  const chat = payload.data.chat
  if (!isRecord(chat) || !isString(chat.id) || !chat.id) return undefined
  return chat.id
}

/**
 * consumeSSE 读取聊天事件流：复用通用 framing，只在此处解释 envelope、
 * 帧结构与 done 语义。错误携带 safe context（conversationId），便于首轮失败仍能关联会话。
 */
export async function consumeSSE(
  body: ReadableStream<Uint8Array>,
  onFrame: (frame: ChatFrame) => void,
  options?: ConversationSSEOptions,
) {
  let done = false
  let currentConversationId = options?.conversationId
  await readEventStream(
    body,
    raw => {
      let payload: unknown
      try {
        payload = JSON.parse(raw)
      } catch {
        throw new ApiRequestError('无法解析 SSE 事件', { kind: 'protocol' })
      }
      if (!isApiEnvelope(payload)) {
        throw new ApiRequestError('SSE 事件格式不正确', { kind: 'protocol' })
      }
      const suppliedId = frameConversationId(payload)
      if (suppliedId && currentConversationId && suppliedId !== currentConversationId) throw new ApiRequestError('SSE 会话身份不匹配', { kind: 'protocol' })
      const conversationId = suppliedId ?? currentConversationId
      const context = conversationId ? { conversationId } : undefined
      if (payload.code !== SUCCESS_CODE) {
        if (isRecord(payload.data) && payload.data.reference_summary != null) {
          const audit = sseFrameGuard(payload.data)
          if (!audit.ok || !suppliedId || audit.value.chat.flag !== 'error') throw new ApiRequestError('失败引用资料格式不正确', { kind: 'protocol', context })
          if (audit.value.reference_summary) options?.onErrorAudit?.(suppliedId, audit.value.reference_summary)
        }
        throw new ApiRequestError(payload.message || '对话生成失败', {
          code: payload.code,
          kind: 'business',
          context,
        })
      }
      const checked = sseFrameGuard(payload.data)
      if (!checked.ok) {
        throw new ApiRequestError(checked.error, { kind: 'protocol', context })
      }
      const frame = checked.value
      if (frame.chat.id) currentConversationId = frame.chat.id
      if (frame.chat.flag === 'error') {
        if (frame.reference_summary && !frame.chat.id) throw new ApiRequestError('失败引用资料缺少会话身份', { kind: 'protocol', context })
        if (frame.chat.id && frame.reference_summary) options?.onErrorAudit?.(frame.chat.id, frame.reference_summary)
        // 成功码 + error flag 属于兼容帧，按协议兜底而不是未知业务码。
        throw new ApiRequestError(frame.chat.content || '对话生成失败', { kind: 'protocol', context })
      }
      onFrame(frame)
      done = frame.chat.flag === 'done'
    },
    { shouldStop: () => done },
  )
  if (!done) {
    throw new ApiRequestError('连接意外中断，未收到对话完成事件；请重新加载历史确认结果', {
      kind: 'network',
      context: options?.conversationId ? { conversationId: options.conversationId } : undefined,
    })
  }
}
