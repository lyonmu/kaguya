import type { Block, ChatFrame, Conversation, ConversationContext, ConversationPage, ConversationTitle, Turn, TurnPage, Usage } from './types'
import { guardFailure, guardSuccess, isArrayOf, isBoolean, isNumber, isOptional, isRecord, isString, type PayloadGuard } from '../../api/http'

// 本模块集中声明网络边界上的必需字段；允许后端追加未知字段，
// 只拒绝缺失、类型错误或明显破坏 reducer 的响应。

// 字段缺失时按 0 处理；出现时必须类型正确，避免 NaN 进入展示层。
const isUsage = (value: unknown, required: boolean): value is Usage => {
  if (!isRecord(value)) return false
  const numbers = ['input_tokens', 'output_tokens', 'total_tokens', 'cached_tokens', 'reasoning_tokens']
  return numbers.some(field => !isOptional(value[field], isNumber)) ? false : required ? isNumber(value.total_tokens) : true
}

const blockTypes = ['text', 'reasoning', 'tool_call', 'tool_result']

export const isBlock = (value: unknown): value is Block =>
  isRecord(value) && isString(value.type) && blockTypes.includes(value.type)

const isTurn = (value: unknown): value is Turn =>
  isRecord(value) &&
  isNumber(value.turn_index) &&
  isString(value.user_content) &&
  isString(value.started_at) &&
  isArrayOf(value.blocks, isBlock) &&
  isOptional(value.usage, (item): item is Usage => isUsage(item, false))

const isConversation = (value: unknown): value is Conversation =>
  isRecord(value) &&
  isString(value.id) &&
  isString(value.title) &&
  isOptional(value.turn_count, isNumber) &&
  isOptional(value.usage, (item): item is Usage => isUsage(item, false))

export const conversationGuard: PayloadGuard<Conversation> = value =>
  isConversation(value) ? guardSuccess(value) : guardFailure('会话数据格式不正确')

// items 是唯一必需字段；total/page 可选，缺失时由调用方按实际长度处理。
export const conversationPageGuard: PayloadGuard<ConversationPage> = value =>
  isRecord(value) && isArrayOf(value.items, isConversation)
    ? guardSuccess(value as unknown as ConversationPage)
    : guardFailure('会话列表格式不正确')

export const turnPageGuard: PayloadGuard<TurnPage> = value =>
  isRecord(value) && isArrayOf(value.items, isTurn) && isNumber(value.page) && isNumber(value.total_pages)
    ? guardSuccess(value as unknown as TurnPage)
    : guardFailure('历史轮次格式不正确')

export const turnGuard: PayloadGuard<Turn> = value =>
  isTurn(value) ? guardSuccess(value) : guardFailure('轮次数据格式不正确')

export const blockGuard: PayloadGuard<Block> = value =>
  isBlock(value) ? guardSuccess(value) : guardFailure('轮次内容格式不正确')

export const conversationTitleGuard: PayloadGuard<ConversationTitle> = value =>
  isRecord(value) && isString(value.id) && isString(value.title)
    ? guardSuccess({ id: value.id, title: value.title })
    : guardFailure('标题数据格式不正确')

export const projectFilesGuard: PayloadGuard<{ files: string[]; truncated: boolean }> = value =>
  isRecord(value) && isArrayOf(value.files, isString) && isBoolean(value.truncated)
    ? guardSuccess({ files: value.files, truncated: value.truncated })
    : guardFailure('文件引用格式不正确')

// 只有 conversation_id 是必需的；可选字段出现时必须类型正确，缺失时展示层按未知/0 处理。
export const conversationContextGuard: PayloadGuard<ConversationContext> = value => {
  if (!isRecord(value) || !isString(value.conversation_id)) {
    return guardFailure('上下文用量格式不正确')
  }
  for (const field of ['model_id', 'model_name'] as const) {
    if (!isOptional(value[field], isString)) return guardFailure('上下文用量格式不正确')
  }
  for (const field of ['turn_index', 'context_window', 'effective_window'] as const) {
    if (!isOptional(value[field], isNumber)) return guardFailure('上下文用量格式不正确')
  }
  return guardSuccess(value as unknown as ConversationContext)
}

const frameFlags = ['start', 'delta', 'done', 'error']

// sseFrameGuard 校验下行帧的关键字段；usage/content/block 可选，但出现时必须结构正确。
// 会话尚未建立时的错误帧允许空 id，与 API 层的失败响应一致。
export const sseFrameGuard: PayloadGuard<ChatFrame> = value => {
  if (!isRecord(value) || !isRecord(value.chat)) return guardFailure('SSE 事件格式不正确')
  const chat = value.chat
  if (!isString(chat.flag) || !frameFlags.includes(chat.flag)) return guardFailure('SSE 事件格式不正确')
  if (!isString(chat.id)) return guardFailure('SSE 事件格式不正确')
  if (chat.content !== undefined && !isString(chat.content)) return guardFailure('SSE 事件格式不正确')
  if (chat.block !== undefined && !isBlock(chat.block)) return guardFailure('SSE 事件格式不正确')
  if (!isOptional(value.usage, (item): item is Usage => isUsage(item, true))) return guardFailure('SSE 事件格式不正确')
  return guardSuccess(value as unknown as ChatFrame)
}
