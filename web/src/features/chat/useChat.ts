import { useEffect, useRef, useState } from 'react'
import { fetchConversation, fetchTurnPage, streamChat, HISTORY_PAGE_SIZE } from './api'
import { applyFrame } from './reducer'
import { syncCompletedConversation } from './completion'
import type { Conversation, ConversationTitle, Turn } from './types'

const errorText = (error: unknown) => error instanceof Error ? error.message : '请求失败，请重试'
interface Session {
  key: string
  id: string
  conversation?: Conversation
  turns: Turn[]
  page: number
  totalPages: number
  initialEnd: boolean
  loading: boolean
  streaming: boolean
  error: string
  stream?: AbortController
  request?: AbortController
  completion?: AbortController
  projectId?: string
  draft: string
  references: string[]
  modelId: string
}
let nextKey = 0
const createSession = (id = ''): Session => ({ key: `session-${++nextKey}`, id, draft: '', references: [], modelId: '', turns: [], page: 1, totalPages: 0, initialEnd: true, loading: false, streaming: false, error: '' })

// Each stream owns its session object. Navigation only changes which object is displayed.
export function useChat(onCompleted: () => Promise<void>, onTitleUpdated: (title: ConversationTitle) => void) {
  const [initial] = useState(createSession)
  const selected = useRef(initial)
  const sessions = useRef(new Map([[initial.key, initial]]))
  const [, render] = useState(0)
  const [viewKey, setViewKey] = useState(0)
  const mounted = useRef(true)
  const callbacks = useRef({ onCompleted, onTitleUpdated })
  callbacks.current = { onCompleted, onTitleUpdated }
  const notify = () => { if (mounted.current) render(value => value + 1) }

  useEffect(() => {
    mounted.current = true
    const all = sessions.current
    return () => {
      mounted.current = false
      all.forEach(session => {
        session.request?.abort()
        session.stream?.abort()
        session.completion?.abort()
      })
    }
  }, [])

  const select = async (id: string) => {
    const previous = selected.current
    previous.request?.abort()
    previous.request = undefined
    previous.loading = false
    let session = id ? [...sessions.current.values()].find(item => item.id === id || item.key === id) : undefined
    if (!session) {
      session = createSession(id)
      sessions.current.set(session.key, session)
    }
    if (!previous.id && !previous.turns.length && !previous.draft && !previous.references.length && !previous.streaming && previous !== session) sessions.current.delete(previous.key)
    selected.current = session
    if (session !== previous) setViewKey(value => value + 1)
    // Completed background results remain visible; explicit reload refreshes persisted history.
    if (session.streaming || (session !== previous && session.turns.length > 0)) { notify(); return }
    session.error = ''
    if (!session.id) { notify(); return }
    const controller = new AbortController()
    session.request = controller
    session.loading = true
    notify()
    try {
      const [detail, history] = await Promise.all([fetchConversation(session.id, controller.signal), fetchTurnPage(session.id, 1000000, controller.signal)])
      if (controller.signal.aborted) return
      session.conversation = detail
      session.projectId = detail.project_id ?? undefined
      session.turns = history.items ?? []
      session.page = history.page
      session.totalPages = history.total_pages
      session.initialEnd = true
    } catch (error) {
      if (!controller.signal.aborted) session.error = errorText(error)
    } finally {
      if (session.request === controller) { session.request = undefined; session.loading = false }
      notify()
    }
  }

  const goToPage = async (page: number, fromEnd = false) => {
    const session = selected.current
    if (session.streaming || session.request || !session.id || page === session.page || page < 1 || page > session.totalPages) return
    const controller = new AbortController()
    session.request = controller
    session.loading = true
    notify()
    try {
      const history = await fetchTurnPage(session.id, page, controller.signal)
      if (controller.signal.aborted) return
      session.turns = history.items ?? []
      session.page = history.page
      session.totalPages = history.total_pages
      session.initialEnd = fromEnd
      session.error = ''
      if (selected.current === session) setViewKey(value => value + 1)
    } catch (error) {
      if (!controller.signal.aborted) session.error = errorText(error)
    } finally {
      if (session.request === controller) { session.request = undefined; session.loading = false }
      notify()
    }
  }

  const send = async (text: string, modelId?: string, projectId?: string) => {
    const session = selected.current
    if (!text.trim() || session.stream || session.request) return
    const files = [...session.references]
    session.references = []
    const controller = new AbortController()
    session.stream = controller
    session.streaming = true
    session.projectId = session.conversation ? session.conversation.project_id ?? undefined : session.projectId ?? projectId
    session.error = ''
    let completed = false
    let turn: Turn | undefined
    notify()
    try {
      if (session.id && session.page < session.totalPages) {
        session.loading = true
        notify()
        const history = await fetchTurnPage(session.id, session.totalPages, controller.signal)
        controller.signal.throwIfAborted()
        session.turns = history.items ?? []
        session.page = history.page
        session.initialEnd = true
        if (selected.current === session) setViewKey(value => value + 1)
      }
      session.loading = false
      // Failed/cancelled turns are not part of persisted history or the next turn index.
      session.turns = session.turns.filter(item => !item.status || item.status === 'done')
      turn = {
        turn_index: Math.max(session.conversation?.turn_count ?? 0, session.turns.at(-1)?.turn_index ?? 0) + 1,
        user_content: text.trim(), model_name: '', model_id: '', api_protocol: '',
        started_at: new Date().toISOString(), duration_ms: 0, tool_calls: 0, blocks: [], status: 'streaming',
      }
      session.page = Math.ceil(turn.turn_index / HISTORY_PAGE_SIZE)
      session.totalPages = session.page
      session.initialEnd = true
      session.turns = turn.turn_index % HISTORY_PAGE_SIZE === 1 ? [turn] : [...session.turns, turn]
      notify()
      await streamChat(session.id, text.trim(), controller.signal, frame => {
        if (controller.signal.aborted) return
        if (frame.chat.id) session.id = frame.chat.id
        turn = applyFrame(turn!, frame)
        session.turns = [...session.turns.slice(0, -1), turn]
        completed = frame.chat.flag === 'done'
        notify()
      }, modelId, session.projectId, files)
    } catch (error) {
      const message = controller.signal.aborted ? '已停止生成；本轮可能未保存，可重新加载历史确认' : errorText(error)
      if (turn) session.turns = [...session.turns.slice(0, -1), { ...turn, status: controller.signal.aborted ? 'stopped' : 'error', error: message }]
      else session.error = message
    } finally {
      session.stream = undefined
      session.streaming = false
      session.loading = false
      notify()
      if (completed && mounted.current) {
        session.completion?.abort()
        const completion = new AbortController()
        session.completion = completion
        void syncCompletedConversation(session.id, completion.signal, {
          onDetail: detail => {
            const current = session.conversation
            if (!current || current.turn_count <= detail.turn_count) {
              session.conversation = current?.title && current.title !== '新对话' && detail.title === '新对话' ? { ...detail, title: current.title } : detail
              notify()
            }
          },
          onCompleted: () => callbacks.current.onCompleted(),
          onTitle: title => {
            if (session.conversation) session.conversation = { ...session.conversation, title: title.title }
            callbacks.current.onTitleUpdated(title)
            notify()
          },
        }).catch(error => {
          if (!completion.signal.aborted) { session.error = errorText(error); notify() }
        }).finally(() => { if (session.completion === completion) session.completion = undefined })
      }
    }
  }

  const session = selected.current
  return {
    draft: session.draft, references: session.references, modelId: session.modelId, projectId: session.projectId,
    setDraft: (value: string, projectId?: string) => { selected.current.draft = value; selected.current.projectId ??= projectId; notify() },
    setReferences: (value: string[], projectId?: string) => { selected.current.references = value; selected.current.projectId ??= projectId; notify() },
    setModelId: (value: string) => { selected.current.modelId = value; notify() },
    id: session.id, sessionKey: session.key, viewKey, conversation: session.conversation,
    setConversation: (value: Conversation) => { selected.current.conversation = value; notify() },
    turns: session.turns, loading: session.loading, streaming: session.streaming, error: session.error,
    page: session.page, totalPages: session.totalPages, initialEnd: session.initialEnd,
    // Unsaved and failed new conversations must remain reachable, even before the start frame.
    localSessions: [...sessions.current.values()].filter(item => item.turns.length > 0 || item.draft.trim() || item.references.length).map(item => ({ key: item.key, id: item.id, title: item.conversation?.title || item.turns[0]?.user_content || item.draft || item.references[0], streaming: item.streaming, draft: !item.turns.length, projectId: item.projectId })),
    select, goToPage, send,
    forget: () => { const item = selected.current; if (!item.streaming) { item.completion?.abort(); sessions.current.delete(item.key) } },
    cancelTitleWait: () => selected.current.completion?.abort(),
    stop: () => selected.current.stream?.abort(),
  }
}
