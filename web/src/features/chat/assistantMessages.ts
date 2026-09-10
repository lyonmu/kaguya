import type { ThreadMessageLike } from '@assistant-ui/react'
import type { Turn } from './types'

export function toAssistantMessages(turns: Turn[]): ThreadMessageLike[] {
  return turns.flatMap(turn => [
    { id: `user-${turn.turn_index}`, role: 'user' as const, content: [{ type: 'text' as const, text: turn.user_content }], createdAt: new Date(turn.started_at), metadata: { custom: { turn } } },
    {
      id: `assistant-${turn.turn_index}`, role: 'assistant' as const,
      content: turn.blocks.filter(block => block.type === 'text').map(block => ({ type: 'text' as const, text: block.text ?? '' })),
      status: turn.status === 'streaming' ? { type: 'running' as const } : turn.status === 'error' || turn.status === 'stopped' ? { type: 'incomplete' as const, reason: turn.status === 'error' ? 'error' as const : 'cancelled' as const } : { type: 'complete' as const, reason: 'stop' as const },
      // Preserve the complete ordered trace, including partial tool JSON and media results.
      metadata: { custom: { turn } },
    },
  ])
}

