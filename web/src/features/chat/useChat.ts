import { useEffect, useRef, useState } from 'react'
import { fetchConversation, fetchTurnPage, stopConversation, streamChat, HISTORY_PAGE_SIZE } from './api'
import { applyFrame } from './reducer'
import { syncCompletedConversation, syncStartedConversation } from './completion'
import { isPersistedStatus, isRunningStatus } from './status'
import type { Conversation, ConversationTitle, Turn } from './types'

const errorText = (error: unknown) => error instanceof Error ? error.message : '请求失败，请重试'
// confirmRunningTurnDelay 是断网/休眠后确认终态的延迟；测试可缩短以避免真实等待。
let confirmRunningTurnDelay = 3000

// setConfirmRunningTurnDelay 仅用于测试覆盖确认延迟。
export const setConfirmRunningTurnDelay = (value: number) => { confirmRunningTurnDelay = value }

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
  title?: AbortController
  confirm?: AbortController
  // opVersion 标识会话当前的持久化操作（发送/切页/重载/删除）；延迟确认只在
  // 版本未变化且会话仍注册时应用，避免覆盖更新的轮次或分页。
  opVersion: number
  projectId?: string
  draft: string
  references: string[]
  modelId: string
}
let nextKey = 0
const createSession = (id = ''): Session => ({ key: `session-${++nextKey}`, id, draft: '', references: [], modelId: '', turns: [], page: 1, totalPages: 0, initialEnd: true, loading: false, streaming: false, error: '', opVersion: 0 })

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
  // 会话详情的唯一更新入口：不倒退轮次数，也不让后到的默认标题覆盖已生成标题。
  const applyDetail = (session: Session, detail: Conversation) => {
    const current = session.conversation
    if (current && current.turn_count > detail.turn_count) return
    session.conversation = current?.title && current.title !== '新对话' && detail.title === '新对话' ? { ...detail, title: current.title } : detail
    notify()
  }

  // 断网/休眠后服务端可能稍晚才察觉断开：延迟确认一次，避免历史停留在“生成中”。
  const confirmRunningTurn = async (session: Session, index: number) => {
    session.confirm?.abort()
    const controller = new AbortController()
    session.confirm = controller
    const version = session.opVersion
    try {
      await new Promise(resolve => setTimeout(resolve, confirmRunningTurnDelay))
      if (!mounted.current || controller.signal.aborted) return
      const history = await fetchTurnPage(session.id, 1, controller.signal)
      // 等待期间用户可能发送新轮次、切页、重载或删除会话：旧响应不得整体覆盖
      // 当前状态，只在会话仍注册、版本未变化且有新 stream 时丢弃。
      if (!mounted.current || controller.signal.aborted) return
      if (!sessions.current.has(session.key) || session.streaming || session.opVersion !== version) return
      const persisted = history.items?.find(item => item.turn_index === index)
      if (persisted && !isRunningStatus(persisted.status)) {
        // 只替换被确认的这一轮终态；分页、草稿与其它轮次保持当前状态。
        session.turns = session.turns.map(item => item.turn_index === index ? persisted : item)
        notify()
      }
    } catch {
      // 网络仍不可用：保留本地状态，用户可手动重新加载历史。
    } finally {
      if (session.confirm === controller) session.confirm = undefined
    }
  }

  useEffect(() => {
    mounted.current = true
    const all = sessions.current
    return () => {
      mounted.current = false
      all.forEach(session => {
        session.request?.abort()
        session.stream?.abort()
        session.completion?.abort()
        session.title?.abort()
        session.confirm?.abort()
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
    // 重载会使等待中的延迟确认失效，避免旧响应覆盖刚拉取的历史。
    session.opVersion++
    session.confirm?.abort()
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
      // 断联轮次在服务端被标记后可能短暂显示为 running，短延迟后确认终态。
      const running = session.turns.find(item => isRunningStatus(item.status))
      if (running) void confirmRunningTurn(session, running.turn_index)    } catch (error) {
      if (!controller.signal.aborted) session.error = errorText(error)
    } finally {
      if (session.request === controller) { session.request = undefined; session.loading = false }
      notify()
    }
  }

  const goToPage = async (page: number, fromEnd = false) => {
    const session = selected.current
    if (session.streaming || session.request || !session.id || page === session.page || page < 1 || page > session.totalPages) return
    // 新的用户操作使已排定的延迟确认失效，避免旧响应覆盖刚拉取的分页。
    session.opVersion++
    session.confirm?.abort()
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
    // 发送会使等待中的延迟确认失效，避免其响应覆盖新轮次。
    session.opVersion++
    session.confirm?.abort()
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
      // 发送前先同步最后一页：服务端已持久化的中断/取消轮次也占用轮次索引，
      // 本地临时失败轮次被丢弃后必须重新对齐，避免新轮次索引与服务端不一致。
      if (session.id) {
        session.loading = true
        notify()
        const history = await fetchTurnPage(session.id, session.totalPages || 1000000, controller.signal)
        controller.signal.throwIfAborted()
        session.turns = history.items ?? []
        session.page = history.page
        session.totalPages = history.total_pages
        session.initialEnd = true
        if (selected.current === session) setViewKey(value => value + 1)
      }
      session.loading = false
      // 失败/取消轮次也持久化并占用轮次索引，但不进入模型上下文；
      // 这里只保留已终态或进行中的轮次，丢弃本地临时失败项。
      session.turns = session.turns.filter(item => isPersistedStatus(item.status))
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
        if (frame.chat.flag === 'start' && !session.conversation) {
          // 后端在首轮正文开始生成前已创建会话并启动标题任务：立即刷新列表，
          // 同时异步等待标题更新，不必等整轮完成。后端任务失败时保留“新对话”。
          void callbacks.current.onCompleted()
          const titleController = new AbortController()
          session.title?.abort()
          session.title = titleController
          void syncStartedConversation(session.id, titleController.signal, {
            onDetail: detail => applyDetail(session, detail),
            onTitle: title => {
              if (session.conversation) session.conversation = { ...session.conversation, title: title.title }
              callbacks.current.onTitleUpdated(title)
              notify()
            },
          }).catch(() => {
            // 标题是辅助信息；done 后的 syncCompletedConversation 会再兜底一次。
          }).finally(() => { if (session.title === titleController) session.title = undefined })
        }
        turn = applyFrame(turn!, frame)
        session.turns = [...session.turns.slice(0, -1), turn]
        completed = frame.chat.flag === 'done'
        notify()
      }, modelId, session.projectId, files)
    } catch (error) {
      const message = controller.signal.aborted ? '已停止生成；已产生的内容会保留在历史中' : errorText(error)
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
          onDetail: detail => applyDetail(session, detail),
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
    forget: () => {
      const item = selected.current
      if (item.streaming) return
      item.opVersion++
      item.confirm?.abort()
      item.completion?.abort()
      item.title?.abort()
      sessions.current.delete(item.key)
    },
    cancelTitleWait: () => { selected.current.completion?.abort(); selected.current.title?.abort() },
    stop: () => {
      const item = selected.current
      // 点击时立即捕获本轮 controller 与身份；等待停止通知期间用户可能已开始下一轮，
      // finally 里不得读取可变的 session.stream，否则会 abort 新轮次。
      const stream = item.stream
      const stoppedSession = item
      if (!stream || stream.signal.aborted) return
      if (item.id) {
        // 先让服务端记录用户主动停止，再断开连接：落库才能区分 canceled 与断联。
        // 网络不可用时最多等 800ms，停止按钮仍要即时生效。
        const notifyStop = stopConversation(item.id).catch(() => {})
        const timer = new Promise(resolve => setTimeout(resolve, 800))
        void Promise.race([notifyStop, timer]).finally(() => {
          // 只中止捕获到的本轮连接；新一轮有自己的 controller。
          if (stoppedSession.stream === stream) stream.abort()
        })
      } else {
        stream.abort()
      }
    },
  }
}
