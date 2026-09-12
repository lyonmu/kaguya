import type { ThreadMessageLike } from '@assistant-ui/react'
import { isFailedStatus, isIncompleteStatus, isRunningStatus } from './status'
import type { Turn } from './types'

export function toAssistantMessages(turns: Turn[]): ThreadMessageLike[] {
  return turns.flatMap(turn => [
    { id: `user-${turn.turn_index}`, role: 'user' as const, content: [{ type: 'text' as const, text: turn.user_content }], createdAt: new Date(turn.started_at), metadata: { custom: { turn } } },
    {
      id: `assistant-${turn.turn_index}`, role: 'assistant' as const,
      content: turn.blocks.filter(block => block.type === 'text').map(block => ({ type: 'text' as const, text: block.text ?? '' })),
      status: isRunningStatus(turn.status)
        ? { type: 'running' as const }
        : isIncompleteStatus(turn.status)
          ? { type: 'incomplete' as const, reason: isFailedStatus(turn.status) ? 'error' as const : 'cancelled' as const }
          : { type: 'complete' as const, reason: 'stop' as const },
      // Preserve the complete ordered trace, including partial tool JSON and media results.
      metadata: { custom: { turn } },
    },
  ])
}
