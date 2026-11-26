import { MEMORY_ENABLED } from '../../app/features'
import { Suspense, lazy, useCallback, useEffect, useRef, useState } from 'react'
import { App, Alert, Button, Drawer, Dropdown, Input, Modal, Popover, Segmented, Spin } from 'antd'
import { CodeOutlined, DeleteOutlined, EditOutlined, MenuOutlined, MessageOutlined, MoreOutlined, PlusOutlined, ReloadOutlined, SearchOutlined } from '@ant-design/icons'
import { useConversations } from '../../features/chat/useConversations'
import { useConversationActions } from '../../features/chat/useConversationActions'
import { useWorkspaceChat } from '../../features/chat/chatContext'
import { VirtualList } from '../../features/chat/components/VirtualList'
import { ConversationRowMenu } from '../../features/chat/components/ConversationRowMenu'
import { MessageList } from '../../features/chat/components/MessageList'
import { AssistantThread } from '../../features/chat/components/AssistantThread'
import { Composer } from '../../features/chat/components/Composer'
import { ApprovalCard } from '../../features/chat/components/ApprovalCard'
import { BottomActions } from '../../components/layout/BottomActions'
import { ProjectPanel } from '../../features/project/ProjectPanel'
import type { Project } from '../../features/project/api'
import { fetchProject } from '../../features/project/api'
import type { ConversationTarget, Reference, TitleJobStatus } from '../../features/chat/types'
import { kaguyaAvatar } from '../../assets/avatars'
import './chat.css'

// 代码浏览器连同 diff 视图库只在打开时加载，避免进入主包。
const CodeBrowserDrawer = lazy(() => import('../../features/code/components/CodeBrowserDrawer').then(module => ({ default: module.CodeBrowserDrawer })))
const titleJobLabels: Record<TitleJobStatus, string> = { pending: '标题排队中', running: '正在生成标题', succeeded: '标题已生成', failed: '标题生成失败', canceled: '标题生成已取消' }

export function ChatPage() {
  const { message } = App.useApp()
  const [view, setView] = useState('对话')
  const [project, setProject] = useState<Project>()
  const [target, setTarget] = useState<ConversationTarget>()
  const projectConversations = useRef<{ projectId: string; items: readonly ConversationTarget[] }>(undefined)
  const trackProjectConversations = useCallback((projectId: string, items: readonly ConversationTarget[]) => {
    projectConversations.current = { projectId, items }
  }, [])
  const sessions = useConversations(view === '项目' ? project?.id : undefined)
  const [projectVersion, setProjectVersion] = useState(0)
  const refreshProjects = () => setProjectVersion(value => value + 1)
  const chat = useWorkspaceChat(async () => { await sessions.refreshQuietly(); refreshProjects() }, title => { sessions.updateTitle(title); refreshProjects() })
  const { draft, references, modelId, setModelId } = chat
  const pendingApprovals = chat.approvals.filter(approval => approval.status === 'pending')
  const { saving, update, remove, upgradeInstructions, generateTitle } = useConversationActions({
    chat, sessions, onProjectsChanged: refreshProjects,
    onRemoved: id => setTarget(current => current?.id === id || (current === target && visibleTarget?.id === id) ? undefined : current),
  })
  const changePermission = (mode: 'full_approval' | 'auto_approval' | 'full_access') => {
    if (chat.conversation) { void update({ permission_mode: mode }); return }
    chat.setPermissionMode(mode)
  }
  const setDraft = (value: string) => chat.setDraft(value, view === '项目' ? project?.id : undefined)
  const setReferences = (value: Reference[]) => chat.setReferences(value, view === '项目' ? project?.id : undefined)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [codeOpen, setCodeOpen] = useState(false)
  const [showSidebar, setShowSidebar] = useState(false)
  const [renaming, setRenaming] = useState(false)
  const [title, setTitle] = useState('')
  const select = (id: string) => {
    if (saving) return
    setTarget(undefined)
    setShowSidebar(false)
    void chat.select(id)
  }
  const selectLocal = (item: typeof chat.localSessions[number]) => {
    if (saving) return
    const id = item.id || item.key
    setView(item.projectId ? '项目' : '对话')
    setProject(current => current?.id === item.projectId ? current : undefined)
    sessions.resetFilters()
    setTarget({ id, title: item.title, projectId: item.projectId })
    setShowSidebar(false)
    void chat.select(id)
  }
  useEffect(() => {
    if (!target?.projectId) return
    let active = true
    void fetchProject(target.projectId).then(value => {
      if (active) setProject(value)
    }).catch(error => { if (active) void message.error(error instanceof Error ? error.message : '加载项目失败') })
    return () => { active = false }
  }, [target, message])
  const send = async (text = draft) => {
    if ((view === '项目' && !project && !chat.projectId && !chat.turns.length) || !text.trim() || chat.streaming || chat.loading || saving || (chat.id && !chat.conversation && !chat.turns.length)) return
    setDraft('')
    await chat.send(text, modelId, chat.conversation ? chat.conversation.project_id ?? undefined : chat.projectId ?? (view === '项目' ? project?.id : undefined))
  }
  const chooseProject = (value?: Project) => {
    if (saving) return
    setTarget(undefined)
    setProject(value)
    void chat.select('')
    sessions.search('')
    sessions.refresh()
  }
  const local = chat.localSessions.filter(item => item.streaming || !sessions.items.some(saved => saved.id === item.id))
  const streamingConversationIds = new Set(chat.localSessions.filter(item => item.streaming).map(item => item.id))
  if (chat.streaming) streamingConversationIds.add(chat.id)
  const targetSession = target && chat.localSessions.find(item => item.id === target.id || item.key === target.id)
  const visibleTarget = targetSession ? { ...target, id: targetSession.id || targetSession.key, title: targetSession.title } : target
  const extraConversationItem = visibleTarget && !visibleTarget.projectId && !sessions.items.some(item => item.id === visibleTarget.id)
    ? { id: visibleTarget.id, title: visibleTarget.title, model_name: '', last_message_at: '' } : undefined
  const conversationItems = extraConversationItem ? [extraConversationItem, ...sessions.items] : sessions.items
  const removeFromList = (id: string, items: readonly ConversationTarget[]) => {
    const index = items.findIndex(item => item.id === id)
    const next = index < 0 ? items[0] : items[index + 1] ?? items[index - 1]
    remove(id, next?.id ?? '')
  }
  const removeFromWorkspace = (id: string, projectId?: string | null) => {
    const projectList = projectConversations.current
    const items = projectId
      ? (projectList?.projectId === projectId && projectList.items.length ? projectList.items : sessions.items.filter(item => item.project_id === projectId))
      : conversationItems
    removeFromList(id, items)
  }
  const showConversations = view === '对话' || !!project || !!chat.projectId || !!chat.turns.length
  const activeProjectId = chat.conversation ? chat.conversation.project_id : chat.projectId ?? (view === '项目' ? project?.id : undefined)
  const sidebar = <div className="chat-sidebar-inner">
    <div className="chat-sidebar-head"><div className="chat-sidebar-title"><h2>对话管理</h2><Button size="small" icon={<PlusOutlined />} disabled={saving || !showConversations} onClick={() => select('')}>新建对话</Button></div>
      {view === '对话' && <Input aria-label="搜索对话标题前缀" placeholder="搜索对话（标题前缀）" prefix={<SearchOutlined />} value={sessions.keyword} onChange={event => sessions.search(event.target.value)} allowClear maxLength={200} />}
      <Segmented size="small" value={view} options={['对话', '项目']} disabled={saving} onChange={value => { setView(value); chooseProject(undefined) }} />
    </div>
    {view === '项目' && <ProjectPanel selected={project} disabled={saving} onSelect={chooseProject} activeId={chat.id || chat.sessionKey} reveal={target} localTarget={visibleTarget} refreshVersion={projectVersion} onSelectedConversations={trackProjectConversations} onConversationRemove={removeFromList} streamingConversationIds={streamingConversationIds} onConversationSelect={(value, id) => { setProject(value); select(id) }} />}
    {view === '对话' && <><div className="chat-session-status">
      {sessions.error && <Alert type="error" title={sessions.error} action={<Button size="small" onClick={sessions.refresh}>重试</Button>} />}
      {sessions.titleError && <Alert type="warning" title={sessions.titleError} action={<Button size="small" onClick={sessions.refresh}>重新加载</Button>} />}
      {sessions.loading && !sessions.items.length && <div className="chat-center"><Spin size="small" /></div>}
      {!sessions.loading && !sessions.error && !conversationItems.length && <p className="chat-center chat-muted">暂无对话</p>}
    </div>
    <VirtualList key={`${sessions.keyword}:${project?.id}:${view}`} className="chat-session-list" items={conversationItems} itemKey={item => item.id} estimate={76} onEnd={sessions.loadMore} reveal={target && { key: visibleTarget!.id, request: target }} renderItem={item => <div className="chat-session-row">
      <button className={`chat-session ${(chat.id || chat.sessionKey) === item.id ? 'active' : ''}`} disabled={saving} onClick={() => select(item.id)}><span className="chat-session-title"><MessageOutlined className="session-icon" aria-hidden="true" /><span className="chat-session-text">{item.title}</span></span><span className="chat-session-meta"><span>{item.model_name || '默认模型'}</span>{item.last_message_at && <time>{new Date(item.last_message_at).toLocaleDateString()}</time>}</span></button>
      {(item !== extraConversationItem || targetSession?.id) && <ConversationRowMenu title={item.title} disabled={saving || streamingConversationIds.has(item.id)} onRemove={() => removeFromList(item.id, conversationItems)} />}
    </div>} /></>}
    {local.length > 0 && <div className="chat-local-sessions" aria-label="本地会话"><h3>进行中与最近会话</h3>{local.map(item => <div className="chat-session-row" key={item.key}>
      <button className={`chat-session ${item.key === chat.sessionKey ? 'active' : ''}`} disabled={saving} onClick={() => selectLocal(item)}><span className="chat-session-title"><MessageOutlined className="session-icon" aria-hidden="true" /><span className="chat-session-text">{item.streaming ? '◌ ' : ''}{item.title}</span></span><span className="chat-session-meta">{item.streaming ? '正在运行' : item.draft ? '草稿' : '查看结果'}{item.projectId ? ' · 项目' : ''}</span></button>
      {item.id && <ConversationRowMenu title={item.title} disabled={saving || item.streaming} onRemove={() => removeFromWorkspace(item.id, item.projectId)} />}
    </div>)}</div>}
    <div className="chat-sidebar-footer"><BottomActions onRefresh={sessions.refresh} loading={sessions.loading} /></div>
  </div>
  return <div className="chat-workspace">
    {!sidebarCollapsed && <aside className="chat-sidebar">{sidebar}</aside>}
    <Drawer title="对话管理" placement="left" open={showSidebar} onClose={() => setShowSidebar(false)} styles={{ body: { padding: 0 } }}>{sidebar}</Drawer>
    <section className="chat-main">
      <header className="chat-header"><div className="chat-header-title"><Button className="chat-desktop-menu" type="text" aria-label={sidebarCollapsed ? '展开会话列表' : '收起会话列表'} aria-expanded={!sidebarCollapsed} icon={<MenuOutlined />} onClick={() => setSidebarCollapsed(value => !value)} /><Button className="chat-mobile-menu" type="text" aria-label="打开会话列表" icon={<MenuOutlined />} onClick={() => setShowSidebar(true)} /><img className="chat-header-avatar" {...kaguyaAvatar} alt="Kaguya" width={26} height={26} /><h1>{chat.conversation?.title || (project ? `${project.name} · 新对话` : '新对话')}</h1>{chat.streaming && <span className="chat-pill is-running" role="status">正在生成</span>}{chat.conversation?.title === '新对话' && chat.conversation.title_job_status && <span className="chat-pill" role="status">{titleJobLabels[chat.conversation.title_job_status]}</span>}{MEMORY_ENABLED && (chat.conversation?.memory_refs?.items.length ?? 0) > 0 && <Popover trigger="click" placement="bottom" title="本轮记忆目录" content={<div className="chat-memory-directory"><p>模型获得的标题与说明。正文是否读取，请查看 memory_read 执行记录。</p><ul>{chat.conversation!.memory_refs!.items.map(item => <li key={item.page_id}><strong>{item.deleted ? '已删除的记忆' : item.title}</strong><small>v{item.version}{item.deleted ? ' · 已删除' : ''}</small></li>)}</ul></div>}><Button type="text" className="chat-memory-trigger">本轮提供 {chat.conversation!.memory_refs!.items.length} 条记忆目录</Button></Popover>}</div>
        <div className="chat-header-actions">
          {activeProjectId && <Button type="text" aria-label="查看代码与改动" icon={<CodeOutlined />} disabled={!chat.id || chat.streaming || saving || chat.loading} onClick={() => setCodeOpen(true)} />}
          <Dropdown key={chat.sessionKey} menu={{ items: [...(activeProjectId ? [{ key: 'code', label: '查看代码与改动', icon: <CodeOutlined /> }] : []), { key: 'reload', label: '重新加载历史', icon: <ReloadOutlined /> }, { key: 'generate-title', label: '补生成标题', disabled: chat.conversation?.title !== '新对话' }, { key: 'rename', label: '重命名', icon: <EditOutlined />, disabled: !chat.conversation }, ...(MEMORY_ENABLED ? [{
            key: 'memory', label: '本会话记忆', disabled: !chat.conversation,
            children: [
              { key: 'memory-inherit', label: '继承全局' },
              { key: 'memory-readonly', label: '只读（不自动学习）' },
              { key: 'memory-off', label: '关闭（本会话不记忆）' },
            ],
          }] : []), { key: 'delete', label: '删除对话', danger: true, icon: <DeleteOutlined />, disabled: !chat.conversation }], onClick: ({ key }) => {
            if (key === 'code') { setCodeOpen(true); return }
            if (key === 'reload') void chat.select(chat.id)
            if (key === 'generate-title') { void generateTitle(); return }
            if (key === 'rename') { setTitle(chat.conversation?.title ?? ''); setRenaming(true) }
            if (key === 'delete') { removeFromWorkspace(chat.id, chat.conversation?.project_id); return }
            if (key.startsWith('memory-')) void update({ memory_mode: key.slice('memory-'.length) })
          } }}><Button aria-label="更多会话操作" type="text" icon={<MoreOutlined />} disabled={!chat.id || chat.streaming || saving || chat.loading} /></Dropdown>
        </div>
      </header>
      {chat.error && <Alert type="error" title={chat.error} showIcon />}
      {chat.titleError && <Alert type="warning" title={chat.titleError} showIcon />}
      {chat.conversation?.instructions_legacy && <Alert
        type="warning"
        showIcon
        title="该会话仍使用旧指令快照"
        description="旧快照无法自动拆分；升级后归档旧文本并冻结当前项目指令，全局指令从下一轮起动态生效。"
        action={<Button size="small" disabled={chat.streaming || saving} onClick={() => void upgradeInstructions()}>升级指令</Button>}
      />}
      <AssistantThread key={chat.sessionKey} turns={chat.turns} streaming={chat.streaming} disabled={!showConversations || chat.loading || saving} onSend={send} onStop={chat.stop}>
      <MessageList liveApprovals={chat.approvals} activeApprovalIds={pendingApprovals.map(approval => approval.approval_id)} conversationId={chat.id} canRestoreReferences={!!(chat.conversation ? chat.conversation.project_id : chat.projectId)} onRestoreReferences={async turn => { const count = await chat.restoreReferences(turn); if (count) void message.success(`已恢复 ${count} 项引用选择`) }} onContinue={() => void chat.send("继续上一轮尚未完成的任务，从已保存的工具结果接着执行。", modelId, chat.conversation?.project_id ?? project?.id)} key={chat.viewKey} turns={chat.turns} loading={chat.loading} streaming={chat.streaming} page={chat.page} totalPages={chat.totalPages} onPageChange={chat.goToPage} initialEnd={chat.initialEnd} />
      {pendingApprovals.length > 0 && <div className="chat-approvals" aria-label="工具审批">
        {pendingApprovals.map(approval => <ApprovalCard key={approval.approval_id} approval={approval} onDecide={chat.decideApproval} />)}
      </div>}
      <Composer projectId={chat.conversation ? chat.conversation.project_id ?? undefined : chat.projectId ?? (view === "项目" ? project?.id : undefined)} conversationId={chat.conversation?.id} turnCount={chat.conversation?.turn_count} modelId={modelId} onModelChange={setModelId} value={draft} onChange={setDraft} references={references} onReferencesChange={setReferences} permissionMode={chat.permissionMode} onPermissionChange={changePermission} streaming={chat.streaming} disabled={!showConversations || chat.loading || saving || (!!chat.id && !chat.conversation && !chat.turns.length)} />
      </AssistantThread>
      {!showSidebar && <div className={sidebarCollapsed ? 'chat-bottom-actions' : 'chat-bottom-actions chat-bottom-actions-mobile'}><BottomActions onRefresh={sessions.refresh} loading={sessions.loading} /></div>}
    </section>
    <Modal title="重命名对话" open={renaming} confirmLoading={saving} onCancel={() => setRenaming(false)} onOk={() => void update({ title: title.trim() })} okButtonProps={{ disabled: !title.trim() }}><Input aria-label="对话标题" value={title} maxLength={200} onChange={event => setTitle(event.target.value)} /></Modal>
    {codeOpen && activeProjectId && <Suspense fallback={null}><CodeBrowserDrawer open projectId={activeProjectId} projectName={project?.name ?? chat.conversation?.title} onClose={() => setCodeOpen(false)} /></Suspense>}
  </div>
}
