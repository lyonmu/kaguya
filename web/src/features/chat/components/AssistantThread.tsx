import { useMemo, type ReactNode } from 'react'
import { AssistantRuntimeProvider, ThreadPrimitive, useExternalStoreRuntime } from '@assistant-ui/react'
import { toAssistantMessages } from '../assistantMessages'
import type { Turn } from '../types'


export function AssistantThread({ turns, streaming, disabled, onSend, onStop, children }: {
  turns: Turn[]; streaming: boolean; disabled: boolean; onSend: (text: string) => Promise<void>; onStop: () => void; children: ReactNode
}) {
  const messages = useMemo(() => toAssistantMessages(turns), [turns])
  const runtime = useExternalStoreRuntime({
    messages, convertMessage: message => message, isRunning: streaming, isDisabled: disabled,
    onNew: async message => {
      const text = message.content.filter(part => part.type === 'text').map(part => part.text).join('\n')
      await onSend(text)
    },
    onCancel: async () => onStop(),
  })
  return <AssistantRuntimeProvider runtime={runtime}><ThreadPrimitive.Root className="assistant-thread">{children}</ThreadPrimitive.Root></AssistantRuntimeProvider>
}
