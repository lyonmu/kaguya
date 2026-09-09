import { useEffect } from 'react'
import { Alert, Button, Dropdown, Spin } from 'antd'
import { FolderOpenOutlined, MoreOutlined } from '@ant-design/icons'
import { useConversations } from '../chat/useConversations'
import type { Project } from './api'

export function ProjectGroup({ project, activeId, selected, disabled, version, onSelect, onConversationSelect, onEdit, onDelete }: {
  project: Project; activeId?: string; selected: boolean; disabled: boolean; version: number
  onSelect: () => void; onConversationSelect: (id: string) => void; onEdit: () => void; onDelete: () => void
}) {
  const sessions = useConversations(project.id)
  const refresh = sessions.refresh
  useEffect(() => { refresh() }, [version, refresh])
  return <section className="project-group" aria-label={`项目 ${project.name}`}>
    <div className="project-group-header">
      <button className={`project-group-name ${selected ? 'selected' : ''}`} title={`${project.path}${project.description ? `\n${project.description}` : ''}`} disabled={disabled} onClick={onSelect}><FolderOpenOutlined /><span>{project.name}</span></button>
      <Dropdown menu={{ items: [{ key: 'new', label: '新建对话' }, { key: 'edit', label: '编辑项目' }, { key: 'delete', label: '删除项目', danger: true }], onClick: ({ key }) => { if (key === 'new') onSelect(); if (key === 'edit') onEdit(); if (key === 'delete') onDelete() } }}><Button type="text" aria-label={`${project.name} 项目操作`} icon={<MoreOutlined />} disabled={disabled} /></Dropdown>
    </div>
    {sessions.error && <Alert type="error" title={sessions.error} action={<Button onClick={refresh}>重试</Button>} />}
    {sessions.loading && !sessions.items.length && <Spin size="small" />}
    <div className="project-conversations">{sessions.items.map(item => <button key={item.id} className={`project-conversation ${item.id === activeId ? 'active' : ''}`} disabled={disabled} onClick={() => onConversationSelect(item.id)} title={item.title}>{item.title}</button>)}
      {sessions.items.length < sessions.total && <Button type="text" loading={sessions.loading} onClick={sessions.loadMore}>加载更多对话</Button>}
    </div>
  </section>
}
