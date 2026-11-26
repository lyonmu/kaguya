import { useEffect, useRef, useState } from 'react'
import { decideApproval, fetchApprovals, fetchConversation, fetchTurnPage, generateConversationTitle, stopConversation, streamChat, HISTORY_PAGE_SIZE } from './api'
import { referenceSelections, referenceSummaryGuard } from './referenceSummary'
import { applyFrame } from './reducer'
import { syncCompletedConversation, syncStartedConversation } from './service'
import { createSession, mergeDetail, normalizedPermissionMode, upsertApproval, type Session } from './session'
import { isPersistedStatus, isRunningStatus } from './status'
import type { Conversation, ConversationTitle, PermissionMode, Reference, TitleUpdate, Turn } from './types'
import { mergeTitle, titleFromConversation } from './titleMerge'

const errorText = (error: unknown) => error instanceof Error ? error.message : '请求失败，请重试'
// confirmRunningTurnDelay 是断网/休眠后确认终态的延迟；测试可缩短以避免真实等待。
let confirmRunningTurnDelay = 3000

// setConfirmRunningTurnDelay 仅用于测试覆盖确认延迟。
export const setConfirmRunningTurnDelay = (value: number) => { confirmRunningTurnDelay = value }

// Each stream owns its session object. Navigation only changes which object is displayed.
export function useChat(onCompleted: () => Promise<void>, onTitleUpdated: (title: ConversationTitle) => void, onTitleConflict?: (id: string) => void) {
  const [initial] = useState(createSession)
  const selected = useRef(initial)
  const sessions = useRef(new Map([[initial.key, initial]]))
  const [, render] = useState(0)
  const [viewKey, setViewKey] = useState(0)
  const [titleError, setTitleError] = useState('')
  const mounted = useRef(true)
  const callbacks = useRef({ onCompleted, onTitleUpdated, onTitleConflict })
  callbacks.current = { onCompleted, onTitleUpdated, onTitleConflict }
  const notify = () => { if (mounted.current) render(value => value + 1) }
  // 会话详情的唯一更新入口：不倒退轮次数，也不让后到的默认标题覆盖已生成标题。
  const applyDetail = (session: Session, detail: Conversation) => {
    if (detail.id !== session.id) return false
    const merged = mergeDetail(session.conversation, detail)
    let titleConfirmed = detail.title_revision !== undefined && detail.title_job_revision !== undefined
    if (session.conversation) {
      const title = mergeTitle(session.conversation, titleFromConversation(detail))
      titleConfirmed = detail.title_revision !== undefined && detail.title_job_revision !== undefined && !title.conflict && detail.title_revision >= (session.conversation.title_revision ?? 0) && detail.title_job_revision >= (session.conversation.title_job_revision ?? 0)
      if (!merged) session.conversation = title.value
      if (title.conflict) callbacks.current.onTitleConflict?.(session.id)
    }
    if (!merged) { notify(); return titleConfirmed }
    const previousProject = session.projectId ?? session.conversation?.project_id
    session.conversation = merged
    session.projectId = merged.project_id ?? undefined
    if (previousProject && previousProject !== session.projectId) {
      // 项目删除只解除归属；保留历史与草稿正文，失效旧工作区的引用选择。
      session.references = []
      session.draftVersion++
    }
    const mode = normalizedPermissionMode(detail.permission_mode)
    if (mode) session.permissionMode = mode
    notify()
    return titleConfirmed
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
      const history = await fetchTurnPage(session.id, 1000000, controller.signal)
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

  // 仅由用户明确补生成调用；普通导航和轮次不进入30秒等待接口。
  const refreshDefaultTitle = async (session: Session) => {
    if (session.conversation?.title !== '新对话') return
    session.title?.abort()
    const controller = new AbortController()
    session.title = controller
    const version = session.opVersion
    try {
      const title = await generateConversationTitle(session.id, controller.signal)
      if (!mounted.current || controller.signal.aborted || session.title !== controller || session.opVersion !== version || !sessions.current.has(session.key) || title.id !== session.id) return
      if (session.conversation) {
        const merged = mergeTitle(session.conversation, title)
        session.conversation = merged.value
        if (merged.conflict) callbacks.current.onTitleConflict?.(session.id)
        callbacks.current.onTitleUpdated({ ...titleFromConversation(merged.value), title: merged.value.title })
        notify()
      }
    } finally { if (session.title === controller) session.title = undefined }
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
    previous.title?.abort()
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
    if (session.streaming) {
      notify()
      return
    }
    // 缓存历史保持原页，仅重新确认可被项目删除等操作改变的服务端归属/权限。
    const cachedHistory = session !== previous && session.turns.length > 0
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
      const [detail, history] = await Promise.all([fetchConversation(session.id, controller.signal), cachedHistory ? Promise.resolve(undefined) : fetchTurnPage(session.id, 1000000, controller.signal)])
      if (controller.signal.aborted) return
      if (detail.id !== session.id) throw new Error('会话身份不匹配')
      if (applyDetail(session, detail)) setTitleError('')
      if (!history) return
      // 刷新后补齐当前 pending 审批；轮次结束时服务端已终结它们。
      void fetchApprovals(session.id, controller.signal).then(items => {
        if (controller.signal.aborted) return
        for (const item of items) session.approvals = upsertApproval(session.approvals, item)
        notify()
      }).catch(() => {})
      // 续聊默认沿用最近一轮模型，用户手动选择后不再覆盖。
      if (!session.modelSeeded) {
        session.modelSeeded = true
        if (detail.last_model_id) session.modelId = detail.last_model_id
      }
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
    session.completion?.abort()
    session.title?.abort()
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
    session.completion?.abort()
    session.title?.abort()
    session.confirm?.abort()
    const version = session.opVersion
    const draftVersion = session.draftVersion
    const references = session.references.map(item => ({ ...item }))
    session.references = []
    session.approvals = new Map()
    const controller = new AbortController()
    session.stream = controller
    session.streaming = true
    session.projectId = session.conversation ? session.conversation.project_id ?? undefined : session.projectId ?? projectId
    session.error = ''
    const sentProjectId = session.projectId
    const ownsTurn = () => session.stream === controller && session.opVersion === version && sessions.current.has(session.key)
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
        if (!ownsTurn()) return
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
      const intent = references.length && sentProjectId ? referenceSummaryGuard({ preparation_state: 'requested', sources: references.map(({ type, path }) => ({ type, path })) }) : undefined
      if (intent && !intent.ok) throw new Error(intent.error)
      const started: Turn = {
        turn_index: Math.max(session.conversation?.turn_count ?? 0, session.turns.at(-1)?.turn_index ?? 0) + 1,
        user_content: text.trim(), model_name: '', model_id: '', api_protocol: '',
        started_at: new Date().toISOString(), duration_ms: 0, tool_calls: 0, blocks: [], status: 'streaming',
        reference_summary: intent?.ok ? intent.value : undefined,
      }
      turn = started
      session.page = Math.ceil(started.turn_index / HISTORY_PAGE_SIZE)
      session.totalPages = session.page
      session.initialEnd = true
      session.turns = started.turn_index % HISTORY_PAGE_SIZE === 1 ? [started] : [...session.turns, started]
      notify()
      await streamChat(session.id, text.trim(), controller.signal, frame => {
        if (controller.signal.aborted || !ownsTurn()) return
        if (frame.chat.id) session.id = frame.chat.id
        if (frame.approval) {
          // 审批帧不属于轮次内容：只更新审批状态，不进入 reducer。
          session.approvals = upsertApproval(session.approvals, frame.approval)
          notify()
        }
        if (frame.chat.flag === 'start' && !session.conversation) {
          // 首轮start已持久化会话，必要详情与列表刷新不等待标题。
          void callbacks.current.onCompleted()
          const detailController = new AbortController()
          session.completion?.abort()
          session.completion = detailController
          void syncStartedConversation(session.id, detailController.signal, {
            onDetail: detail => { if (mounted.current && session.completion === detailController && session.opVersion === version && sessions.current.has(session.key)) applyDetail(session, detail) },
          }).catch(() => {
            // 首轮仍执行；done后再同步持久化详情。
          }).finally(() => { if (session.completion === detailController) session.completion = undefined })
        }
        turn = applyFrame(turn!, frame)
        session.turns = [...session.turns.slice(0, -1), turn]
        completed = frame.chat.flag === 'done'
        notify()
      }, modelId, sentProjectId, references, session.conversation ? undefined : session.permissionMode, (id, summary) => {
        if (controller.signal.aborted || !ownsTurn() || !turn || (session.id && session.id !== id)) return
        session.id = id
        turn = { ...turn, reference_summary: summary }
        session.turns = session.turns.map(item => item.turn_index === turn!.turn_index ? turn! : item)
        notify()
      })
    } catch (error) {
      if (!ownsTurn()) return
      const message = controller.signal.aborted ? '已停止生成；已产生的内容会保留在历史中' : errorText(error)
      if (turn) session.turns = [...session.turns.slice(0, -1), { ...turn, status: controller.signal.aborted ? 'stopped' : 'error', error: message }]
      else session.error = message
      notify()
      if (mounted.current && references.length && sentProjectId && session.draftVersion === draftVersion) {
        if (!session.id) {
          // 尚未创建会话的失败只归还本地选择；不读取文件，也不授予项目权限。
          if (session.projectId === sentProjectId) session.references = references
        } else {
          const restore = new AbortController()
          session.request = restore
          try {
            const detail = await fetchConversation(session.id, restore.signal)
            if (!restore.signal.aborted && ownsTurn() && session.request === restore && session.draftVersion === draftVersion && detail.id === session.id) {
              applyDetail(session, detail)
              if (detail.project_id === sentProjectId) session.references = references
            }
          } catch {
            // 归属未确认时不自动恢复；历史中的意图仍可显式恢复。
          } finally {
            if (session.request === restore) session.request = undefined
          }
        }
      }
    } finally {
      if (ownsTurn()) {
        session.stream = undefined
        session.streaming = false
        session.loading = false
        notify()
        if (!completed && mounted.current && session.id && turn) void confirmRunningTurn(session, turn.turn_index)
        if (completed && mounted.current) {
          session.completion?.abort()
          const completion = new AbortController()
          session.completion = completion
          const ownsCompletion = () => mounted.current && !completion.signal.aborted && session.completion === completion && session.opVersion === version && sessions.current.has(session.key)
          void syncCompletedConversation(session.id, completion.signal, {
            onDetail: detail => { if (ownsCompletion()) applyDetail(session, detail) },
            onCompleted: async () => { if (ownsCompletion()) await callbacks.current.onCompleted() },
          }).catch(error => {
            if (ownsCompletion()) { session.error = errorText(error); notify() }
          }).finally(() => { if (session.completion === completion) session.completion = undefined })
        }
      }
    }
  }

  const restoreReferences = async (turn: Turn): Promise<number> => {
    const session = selected.current
    const loaded = session.turns.find(item => item.turn_index === turn.turn_index && item.id === turn.id)
    const references = referenceSelections(loaded?.reference_summary)
    if (!loaded || !references.length || !session.id || session.stream || session.request) return 0
    session.opVersion++
    session.confirm?.abort()
    const version = session.opVersion, draftVersion = session.draftVersion
    const id = session.id
    const controller = new AbortController()
    session.request = controller
    session.loading = true
    const ownsRestore = () => mounted.current && !controller.signal.aborted && selected.current === session && sessions.current.has(session.key) && session.opVersion === version && session.request === controller && session.draftVersion === draftVersion
    notify()
    try {
      const detail = await fetchConversation(id, controller.signal)
      if (!ownsRestore()) return 0
      if (detail.id !== id) throw new Error('会话身份不匹配，未恢复引用')
      applyDetail(session, detail)
      if (!detail.project_id) {
        // applyDetail 已使旧工作区选择失效；此处仍是刚确认归属的本次操作。
        session.error = '会话已解除项目归属，不能恢复项目引用'
        return 0
      }
      session.references = references
      session.draftVersion++
      session.error = ''
      return references.length
    } catch (error) {
      if (ownsRestore()) session.error = errorText(error)
      return 0
    } finally {
      if (session.request === controller) { session.request = undefined; session.loading = false }
      notify()
    }
  }

  const session = selected.current
  return {
    draft: session.draft, references: session.references, modelId: session.modelId, projectId: session.projectId,
    setDraft: (value: string, projectId?: string) => { selected.current.draftVersion++; selected.current.draft = value; selected.current.projectId ??= projectId; notify() },
    setReferences: (value: Reference[], projectId?: string) => { selected.current.draftVersion++; selected.current.references = value; selected.current.projectId ??= projectId; notify() },
    setModelId: (value: string) => { selected.current.modelId = value; notify() },
    approvals: [...session.approvals.values()],
    permissionMode: session.permissionMode,
    setPermissionMode: (mode: PermissionMode) => { selected.current.permissionMode = mode; notify() },
    decideApproval: async (approvalId: string, decision: 'approve' | 'reject') => {
      const current = selected.current
      if (!current.id) return
      const result = await decideApproval(current.id, approvalId, decision)
      const approval = current.approvals.get(approvalId)
      if (approval) current.approvals = upsertApproval(current.approvals, { ...approval, status: result.status })
      notify()
    },
    id: session.id, sessionKey: session.key, viewKey, conversation: session.conversation,
    setConversation: (value: Conversation) => { const item = [...sessions.current.values()].find(item => item.id === value.id); if (item) applyDetail(item, value) },
    turns: session.turns, loading: session.loading, streaming: session.streaming, error: session.error, titleError,
    page: session.page, totalPages: session.totalPages, initialEnd: session.initialEnd,
    // Unsaved and failed new conversations must remain reachable, even before the start frame.
    localSessions: [...sessions.current.values()].filter(item => item.turns.length > 0 || item.draft.trim() || item.references.length).map(item => ({ key: item.key, id: item.id, title: item.conversation?.title || item.turns[0]?.user_content || item.draft || item.references[0]?.path || '', streaming: item.streaming, draft: !item.turns.length, projectId: item.projectId })),
    knownConversationIDs: [...sessions.current.values()].filter(item => item.conversation).map(item => item.id),
    select, goToPage, send, restoreReferences,
    generateTitle: () => refreshDefaultTitle(selected.current),
    // 事件订阅到确认：更新所有已知会话中匹配当前会话的标题，并取消不再需要的等待。
    applyTitleEvent: (update: TitleUpdate) => {
      let conflict = false
      for (const item of sessions.current.values()) {
        if (item.id !== update.id || !item.conversation) continue
        const merged = mergeTitle(item.conversation, update)
        conflict = conflict || merged.conflict
        item.conversation = merged.value
        if (merged.value.title !== '新对话') item.title?.abort()
      }
      notify()
      return conflict
    },
    titleSyncError: (message: string) => { if (mounted.current) setTitleError(message) },
    removeConversation: (id: string, nextId?: string) => {
      for (const item of sessions.current.values()) {
        if (item.id !== id) continue
        item.opVersion++
        item.request?.abort(); item.stream?.abort(); item.completion?.abort(); item.title?.abort(); item.confirm?.abort()
        sessions.current.delete(item.key)
        if (selected.current === item) {
          if (nextId !== undefined) {
            setTitleError('')
            void select(nextId)
            return
          }
          const replacement = createSession()
          replacement.error = '会话已删除或不可用'
          sessions.current.set(replacement.key, replacement)
          selected.current = replacement
          setViewKey(value => value + 1)
        }
      }
      notify()
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
