import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchConversation, fetchTurnPage, streamChat } from './api'
import { applyFrame } from './reducer'
import { syncCompletedConversation } from './completion'
import type { Conversation, ConversationTitle, Turn } from './types'

const errorText = (error: unknown) => error instanceof Error ? error.message : '请求失败，请重试'

export function useChat(onCompleted: () => Promise<void>, onTitleUpdated: (title: ConversationTitle) => void) {
  const [id, setId] = useState('')
  const selectedId = useRef('')
  const [viewKey, setViewKey] = useState(0)
  const [conversation, setConversation] = useState<Conversation>()
  const [turns, setTurns] = useState<Turn[]>([])
  const [loading, setLoading] = useState(false)
  const [streaming, setStreaming] = useState(false)
  const [error, setError] = useState('')
  const [page, setPage] = useState(1)
  const [totalPages, setTotalPages] = useState(0)
  const [initialEnd, setInitialEnd] = useState(true)
  const request = useRef<AbortController | null>(null)
  const stream = useRef<AbortController | null>(null)
  const completions = useRef(new Set<AbortController>())
  const generation = useRef(0)
  const cancelTitleWait = useCallback(() => {
    completions.current.forEach(controller => controller.abort())
    completions.current.clear()
  }, [])

  useEffect(() => () => {
    generation.current++
    request.current?.abort()
    stream.current?.abort()
    cancelTitleWait()
  }, [cancelTitleWait])

  const select = useCallback(async (nextId: string) => {
    if (stream.current) return
    const token = ++generation.current
    request.current?.abort()
    cancelTitleWait()
    const controller = new AbortController()
    request.current = controller
    if (selectedId.current !== nextId) {
      selectedId.current = nextId
      setId(nextId)
      setViewKey(value => value + 1)
      setConversation(undefined)
      setTurns([])
      setPage(1)
      setTotalPages(0)
      setInitialEnd(true)
    }
    // Refreshing this conversation retains its title/messages until data arrives.
    setError('')
    setLoading(!!nextId)
    if (!nextId) { request.current = null; return }
    try {
      const [detail, history] = await Promise.all([fetchConversation(nextId, controller.signal), fetchTurnPage(nextId, 1000000, controller.signal)])
      if (token !== generation.current) return
      setConversation(detail)
      setTurns(history.items ?? [])
      setPage(history.page)
      setTotalPages(history.total_pages)
      setInitialEnd(true)
    } catch (error) {
      if (!controller.signal.aborted) setError(errorText(error))
    } finally {
      if (request.current === controller) request.current = null
      if (token === generation.current) setLoading(false)
    }
  }, [cancelTitleWait])

  const goToPage = async (nextPage: number, fromEnd = false) => {
    if (stream.current || request.current || loading || !id || nextPage === page || nextPage < 1 || nextPage > totalPages) return
    const token = generation.current
    const controller = new AbortController()
    request.current = controller
    setLoading(true)
    try {
      const history = await fetchTurnPage(id, nextPage, controller.signal)
      if (token !== generation.current || controller.signal.aborted) return
      setTurns(history.items ?? [])
      setPage(history.page)
      setTotalPages(history.total_pages)
      setInitialEnd(fromEnd)
      setViewKey(value => value + 1)
      setError('')
    } catch (error) {
      if (!controller.signal.aborted) setError(errorText(error))
    } finally {
      if (request.current === controller) request.current = null
      if (token === generation.current) setLoading(false)
    }
  }

  const send = async (text: string, modelId?: string, projectId?: string) => {
    if (!text.trim() || stream.current || request.current || loading) return
    const controller = new AbortController()
    stream.current = controller
    const token = generation.current
    let activeId = id
    let completed = false
    // 从历史页续聊时先切回末页；请求仍由后端恢复完整上下文。
    if (id && page < totalPages) {
      setLoading(true)
      try {
        const history = await fetchTurnPage(id, totalPages, controller.signal)
        if (token !== generation.current) return
        setTurns(history.items)
        setPage(history.page)
        setInitialEnd(true)
        setViewKey(value => value + 1)
      } catch (error) {
        if (!controller.signal.aborted) setError(errorText(error))
        stream.current = null
        setLoading(false)
        return
      }
      setLoading(false)
    }
    const turn: Turn = {
      turn_index: Math.max(conversation?.turn_count ?? 0, turns.at(-1)?.turn_index ?? 0) + 1,
      user_content: text.trim(), model_name: '', model_id: '', api_protocol: '',
      started_at: new Date().toISOString(), duration_ms: 0, tool_calls: 0, blocks: [], status: 'streaming',
    }
    const nextPage = Math.ceil(turn.turn_index / 20)
    setPage(nextPage)
    setTotalPages(nextPage)
    setInitialEnd(true)
    setTurns(current => turn.turn_index % 20 === 1 ? [turn] : [...current, turn])
    setStreaming(true)
    setError('')
    const updateLast = (update: (turn: Turn) => Turn) => {
      if (token === generation.current) setTurns(current => current.map((item, index) => index === current.length - 1 ? update(item) : item))
    }
    try {
      await streamChat(activeId, text.trim(), controller.signal, frame => {
        if (token !== generation.current) return
        if (frame.chat.id) {
          activeId = frame.chat.id
          selectedId.current = activeId
          setId(activeId)
        }
        updateLast(turn => applyFrame(turn, frame))
        completed = frame.chat.flag === 'done'
      }, modelId, projectId)
    } catch (error) {
      updateLast(turn => ({ ...turn, status: controller.signal.aborted ? 'stopped' : 'error', error: controller.signal.aborted ? '已停止生成；本轮可能未保存，可重新加载历史确认' : errorText(error) }))
    } finally {
      if (token === generation.current) {
        stream.current = null
        setStreaming(false)
        if (completed) {
          const completion = new AbortController()
          completions.current.add(completion)
          void syncCompletedConversation(activeId, completion.signal, {
            onDetail: detail => setConversation(current => {
              if (current && current.turn_count > detail.turn_count) return current
              // A concurrently completed title must not regress to a stale default.
              return current?.title && current.title !== '新对话' && detail.title === '新对话'
                ? { ...detail, title: current.title } : detail
            }),
            onCompleted,
            onTitle: title => {
              setConversation(current => current?.id === title.id ? { ...current, title: title.title } : current)
              onTitleUpdated(title)
            },
          }).catch(error => {
            if (!completion.signal.aborted && token === generation.current) setError(errorText(error))
          }).finally(() => completions.current.delete(completion))
        }
      }
    }
  }

  return { id, viewKey, conversation, setConversation, turns, loading, streaming, error, page, totalPages, initialEnd, select, goToPage, send, cancelTitleWait, stop: () => stream.current?.abort() }
}
