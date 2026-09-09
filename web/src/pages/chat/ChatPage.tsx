import { useState } from 'react'
import { App, Alert, Button, Drawer, Dropdown, Input, Modal, Pagination, Segmented, Spin, Tooltip } from 'antd'
import { DeleteOutlined, EditOutlined, MenuOutlined, MoreOutlined, PlusOutlined, ReloadOutlined, SearchOutlined, StarFilled, StarOutlined } from '@ant-design/icons'
import { deleteConversation, updateConversation } from '../../features/chat/api'
import { useConversations } from '../../features/chat/useConversations'
import { useChat } from '../../features/chat/useChat'
import { MessageList } from '../../features/chat/components/MessageList'
import { Composer } from '../../features/chat/components/Composer'
import './chat.css'

export function ChatPage() {
  const { message, modal } = App.useApp()
  const sessions = useConversations()
  const chat = useChat(sessions.refreshQuietly, sessions.updateTitle)
  const [draft, setDraft] = useState('')
  const [modelId, setModelId] = useState('')
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [showSidebar, setShowSidebar] = useState(false)
  const [renaming, setRenaming] = useState(false)
  const [title, setTitle] = useState('')
  const [saving, setSaving] = useState(false)
  const select = (id: string) => {
    if (chat.streaming || saving) return
    setDraft('')
    setShowSidebar(false)
    void chat.select(id)
  }
  const update = async (payload: { title?: string; favorite?: boolean }) => {
    if (payload.title !== undefined) chat.cancelTitleWait()
    setSaving(true)
    try {
      const result = await updateConversation(chat.id, payload)
      chat.setConversation(result)
      await sessions.refreshQuietly()
      setRenaming(false)
    } catch (error) { void message.error(error instanceof Error ? error.message : '更新失败') }
    finally { setSaving(false) }
  }
  const remove = () => modal.confirm({
    title: '删除此对话？', content: '删除后无法恢复，也无法继续对话。', okText: '删除', okButtonProps: { danger: true },
    onOk: async () => {
      chat.cancelTitleWait()
      setSaving(true)
      try {
        await deleteConversation(chat.id)
        await chat.select('')
        sessions.refresh()
      } catch (error) {
        void message.error(error instanceof Error ? error.message : '删除失败')
        throw error
      } finally { setSaving(false) }
    },
  })
  const send = () => {
    if (!draft.trim() || chat.streaming || chat.loading || saving || (chat.id && !chat.conversation)) return
    const text = draft
    setDraft('')
    void chat.send(text, modelId)
  }
  const sidebar = <div className="chat-sidebar-inner">
    <div className="chat-sidebar-head"><div className="chat-sidebar-title"><h2>对话管理</h2><Button size="small" icon={<PlusOutlined />} disabled={chat.streaming || saving} onClick={() => select('')}>新建对话</Button></div>
      <Input aria-label="搜索对话标题前缀" placeholder="搜索对话（标题前缀）" prefix={<SearchOutlined />} value={sessions.keyword} onChange={event => sessions.search(event.target.value)} allowClear maxLength={200} />
      <Segmented size="small" value={sessions.favorite ? '收藏' : '全部'} options={['全部', '收藏']} onChange={value => sessions.filter(value === '收藏')} />
    </div>
    <div className="chat-session-list">
      {sessions.error && <Alert type="error" title={sessions.error} action={<Button size="small" onClick={sessions.refresh}>重试</Button>} />}
      {sessions.loading && !sessions.items.length && <div className="chat-center"><Spin size="small" /></div>}
      {!sessions.loading && !sessions.error && !sessions.items.length && <p className="chat-center chat-muted">暂无{sessions.favorite ? '收藏' : ''}对话</p>}
      {sessions.items.map(item => <button className={`chat-session ${chat.id === item.id ? 'active' : ''}`} key={item.id} disabled={chat.streaming || saving} onClick={() => select(item.id)}><span className="chat-session-title">{item.favorite && <StarFilled />} {item.title}</span><span className="chat-session-meta"><span>{item.model_name || '默认模型'}</span><time>{new Date(item.last_message_at).toLocaleDateString()}</time></span></button>)}
    </div>
    <div className="chat-sidebar-footer"><Pagination simple size="small" current={sessions.page} total={sessions.total} pageSize={20} onChange={sessions.setPage} hideOnSinglePage /><Button type="text" size="small" icon={<ReloadOutlined />} onClick={sessions.refresh}>刷新列表</Button></div>
  </div>
  return <div className="chat-workspace">
    {!sidebarCollapsed && <aside className="chat-sidebar">{sidebar}</aside>}
    <Drawer title="对话管理" placement="left" open={showSidebar} onClose={() => setShowSidebar(false)} styles={{ body: { padding: 0 } }}>{sidebar}</Drawer>
    <section className="chat-main">
      <header className="chat-header"><div className="chat-header-title"><Button className="chat-desktop-menu" type="text" aria-label={sidebarCollapsed ? '展开会话列表' : '收起会话列表'} aria-expanded={!sidebarCollapsed} icon={<MenuOutlined />} onClick={() => setSidebarCollapsed(value => !value)} /><Button className="chat-mobile-menu" type="text" aria-label="打开会话列表" icon={<MenuOutlined />} onClick={() => setShowSidebar(true)} /><h1>{chat.conversation?.title || '新对话'}</h1><span className="chat-pill">SSE</span></div>
        <div className="chat-header-actions"><Tooltip title="收藏"><Button aria-label="收藏对话" type="text" disabled={!chat.conversation || chat.streaming || saving} icon={chat.conversation?.favorite ? <StarFilled /> : <StarOutlined />} onClick={() => void update({ favorite: !chat.conversation?.favorite })} /></Tooltip>
          <Dropdown menu={{ items: [{ key: 'reload', label: '重新加载历史', icon: <ReloadOutlined /> }, { key: 'rename', label: '重命名', icon: <EditOutlined />, disabled: !chat.conversation }, { key: 'delete', label: '删除对话', danger: true, icon: <DeleteOutlined />, disabled: !chat.conversation }], onClick: ({ key }) => {
            if (key === 'reload') void chat.select(chat.id)
            if (key === 'rename') { setTitle(chat.conversation?.title ?? ''); setRenaming(true) }
            if (key === 'delete') remove()
          } }}><Button aria-label="更多会话操作" type="text" icon={<MoreOutlined />} disabled={!chat.id || chat.streaming || saving || chat.loading} /></Dropdown>
        </div>
      </header>
      {chat.error && <Alert type="error" title={chat.error} showIcon />}
      <MessageList key={chat.viewKey} turns={chat.turns} loading={chat.loading} hasMore={chat.hasMore} streaming={chat.streaming} onLoadMore={chat.loadMore} />
      <Composer conversationId={chat.conversation?.id} turnCount={chat.conversation?.turn_count} modelId={modelId} onModelChange={setModelId} value={draft} onChange={setDraft} streaming={chat.streaming} disabled={chat.loading || saving || (!!chat.id && !chat.conversation)} onSend={send} onStop={chat.stop} />
    </section>
    <Modal title="重命名对话" open={renaming} confirmLoading={saving} onCancel={() => setRenaming(false)} onOk={() => void update({ title: title.trim() })} okButtonProps={{ disabled: !title.trim() }}><Input aria-label="对话标题" value={title} maxLength={200} onChange={event => setTitle(event.target.value)} /></Modal>
  </div>
}
