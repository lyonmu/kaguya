import { useEffect } from 'react'
import { Alert, Button, Dropdown, Spin } from 'antd'
import { DownOutlined, FolderOpenOutlined, MessageOutlined, MoreOutlined, RightOutlined } from '@ant-design/icons'
import { useConversations } from '../chat/useConversations'
import { VirtualList } from '../chat/components/VirtualList'
import type { Project } from './api'
import type { ConversationTarget } from '../chat/types'

export function ProjectGroup({ project, expanded, onToggle, activeId, selected, disabled, version, onSelect, onConversationSelect, onEdit, onDelete, reveal, localTarget }: {
  project: Project; expanded: boolean; onToggle: () => void; activeId?: string; selected: boolean; disabled: boolean; version: number
  onSelect: () => void; onConversationSelect: (id: string) => void; onEdit: () => void; onDelete: () => void
  reveal?: ConversationTarget; localTarget?: ConversationTarget
}) {
  // 折叠时不请求会话；展开后按服务端分页逐步加载，滚动到底再取下一页。
  const sessions = useConversations(project.id, { enabled: expanded })
  const refresh = sessions.refresh
  useEffect(() => { refresh() }, [version, refresh])
  const items = localTarget && !sessions.items.some(item => item.id === localTarget.id) ? [localTarget, ...sessions.items] : sessions.items
  const revealKey = localTarget?.id ?? reveal?.id
  return <section className="project-group" aria-label={`项目 ${project.name}`}>
    <div className="project-group-header">
      <button className={`project-group-name ${selected ? 'selected' : ''}`} title={`${project.path}${project.description ? `\n${project.description}` : ''}`} disabled={disabled} onClick={onSelect}><FolderOpenOutlined /><span>{project.name}</span></button>
      <Button type="text" className="project-group-toggle" aria-label={`${expanded ? '收起' : '展开'}项目 ${project.name}`} aria-expanded={expanded} icon={expanded ? <DownOutlined /> : <RightOutlined />} disabled={disabled} onClick={onToggle} />
      <Dropdown menu={{ items: [{ key: 'new', label: '新建对话' }, { key: 'edit', label: '编辑项目' }, { key: 'delete', label: '删除项目', danger: true }], onClick: ({ key }) => { if (key === 'new') onSelect(); if (key === 'edit') onEdit(); if (key === 'delete') onDelete() } }}><Button type="text" aria-label={`${project.name} 项目操作`} icon={<MoreOutlined />} disabled={disabled} /></Dropdown>
    </div>
    {expanded && <>
      {sessions.error && <Alert type="error" title={sessions.error} action={<Button onClick={refresh}>重试</Button>} />}
      {sessions.loading && !sessions.items.length && <Spin size="small" />}
      {!sessions.loading && !sessions.error && !items.length && <p className="project-empty">暂无对话</p>}
      {items.length > 0 && <VirtualList className="project-conversations" items={items} itemKey={item => item.id} estimate={38} onEnd={sessions.loadMore} reveal={revealKey && reveal ? { key: revealKey, request: reveal } : undefined} renderItem={item => <button className={`project-conversation ${item.id === activeId ? 'active' : ''}`} disabled={disabled} onClick={() => onConversationSelect(item.id)} title={item.title}><MessageOutlined className="session-icon" aria-hidden="true" /><span className="project-conversation-text">{item.title}</span></button>} />}
    </>}
  </section>
}
