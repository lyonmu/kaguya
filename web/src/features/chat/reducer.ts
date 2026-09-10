import type { Block, ChatFrame, Turn } from './types'

export function applyFrame(turn: Turn, frame: ChatFrame): Turn {
  const next = { ...turn, model_id: frame.model_id || turn.model_id, model_name: frame.model_name || turn.model_name, api_protocol: frame.api_protocol || turn.api_protocol }
  if (frame.chat.flag === 'done') {
    return { ...next, usage: frame.usage, finish_reason: frame.finish_reason, status: 'done', finished_at: new Date().toISOString(), duration_ms: Date.now() - Date.parse(turn.started_at) }
  }
  if (frame.chat.flag !== 'delta') return next
  const event = frame.chat.block
  if (!event) {
    if (!frame.chat.content) return next
    return applyFrame(turn, { ...frame, chat: { ...frame.chat, block: { type: 'text', phase: 'delta', text: frame.chat.content } } })
  }
  const blocks = turn.blocks.map(block => ({ ...block }))
  if (event.type === 'tool_call' || event.type === 'tool_result') {
    const index = blocks.findIndex(block => block.type === 'tool_call' && block.tool_call_id === event.tool_call_id)
    const block: Block = index < 0 ? { ...event, type: 'tool_call', input: '', started_at: new Date().toISOString() } : blocks[index]
    if (event.type === 'tool_result') {
      Object.assign(block, { output: event.output, is_error: event.is_error, error_message: event.error_message, phase: 'block_end', finished_at: new Date().toISOString() })
    } else {
      block.tool_name = event.tool_name || block.tool_name
      block.input = event.phase === 'block_end' ? (event.input ?? '') : (block.input ?? '') + (event.input ?? '')
      block.phase = event.phase
      block.is_error = event.is_error
      block.error_message = event.error_message
      block.provider_executed = event.provider_executed
    }
    if (index < 0) blocks.push(block)
  } else {
    const last = blocks.at(-1)
    if (event.phase === 'start' || !last || last.type !== event.type || last.phase === 'block_end') {
      blocks.push({ ...event, started_at: new Date().toISOString() })
    } else {
      last.text = (last.text ?? '') + (event.phase === 'delta' ? event.text ?? '' : '')
      last.phase = event.phase
      if (event.phase === 'block_end') last.finished_at = new Date().toISOString()
    }
  }
  return { ...next, blocks, tool_calls: blocks.filter(block => block.type === 'tool_call').length }
}
