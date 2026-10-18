import { useEffect, useState } from 'react'
import { Alert, App, Button, Input, Modal, Space, Spin } from 'antd'
import { FolderOutlined, PlusOutlined } from '@ant-design/icons'
import type { Project, ProjectInput } from './types'
import type { ConversationTarget } from '../chat/types'
import { VirtualList } from '../chat/components/VirtualList'

import { useProjects } from './useProjects'
import { useProjectDirectories } from './useProjectDirectories'

import { ProjectGroup } from './ProjectGroup'

const empty: ProjectInput = { name: '', path: '', description: '' }
const errorText = (error: unknown) => error instanceof Error ? error.message : '项目操作失败'

export function ProjectPanel({ selected, disabled, onSelect, activeId, onConversationSelect, refreshVersion = 0, reveal, localTarget }: { selected?: Project; disabled: boolean; onSelect: (project?: Project) => void; activeId?: string; onConversationSelect?: (project: Project, id: string) => void; refreshVersion?: number; reveal?: ConversationTarget; localTarget?: ConversationTarget }) {
  const { message, modal } = App.useApp()
  const { keyword, setKeyword, items, revision, loading, loadingMore, error, refresh, loadMore, saving, loadDetail, save: saveRecord, remove: removeRecord, cancelOperation } = useProjects()
  const { directories, browsing, directoryError, browse: browseDirectories, cancel: cancelBrowse } = useProjectDirectories()
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set())
  const [editing, setEditing] = useState<string | null>(null)
  const [form, setForm] = useState<ProjectInput>(empty)

  useEffect(() => {
    if (reveal) setKeyword('')
  }, [reveal, setKeyword])
  useEffect(() => {
    if (!selected) return
    setExpanded(current => current.has(selected.id) ? current : new Set(current).add(selected.id))
  }, [selected])
  const toggle = (id: string) => setExpanded(current => {
    const next = new Set(current)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    return next
  })
  const select = (project: Project) => {
    setExpanded(current => current.has(project.id) ? current : new Set(current).add(project.id))
    onSelect(project)
  }
  const browse = async (path = '', selectDirectory = true) => {
    const result = await browseDirectories(path)
    if (result && selectDirectory) setForm(current => ({ ...current, path: result.path, name: !current.name || current.name === current.path.split('/').filter(Boolean).at(-1) ? result.path.split('/').filter(Boolean).at(-1) || '' : current.name }))
  }
  const edit = async (project?: Project) => {
    try {
      cancelBrowse()
      if (!project) cancelOperation()
      const detail = project ? await loadDetail(project.id) : undefined
      if (project && !detail) return
      setForm(detail ?? empty)
      setEditing(detail?.id ?? '')
      void browse('', !detail) // Editing keeps the saved path until the user navigates.
    } catch (error) { void message.error(errorText(error)) }
  }
  const save = async () => {
    try {
      const project = await saveRecord(editing ?? '', { ...form, name: form.name.trim() })
      if (!project) return
      setEditing(null)
      cancelBrowse()
      select(project)
    } catch (error) { void message.error(errorText(error)) }
  }
  const remove = (project: Project) => modal.confirm({
    title: `删除项目“${project.name}”？`, content: '仅删除项目记录；对话解除项目归属并保留在普通对话列表，主机文件不会删除。', okText: '删除', okButtonProps: { danger: true },
    onOk: async () => {
      try { if (await removeRecord(project.id) && selected?.id === project.id) onSelect(undefined) }
      catch (error) { void message.error(errorText(error)); throw error }
    },
  })
  // Keep a directly selected project reachable even when it is outside the loaded page.
  const visibleItems = reveal && selected && selected.id === reveal.projectId && !items.some(item => item.id === selected.id) ? [selected, ...items] : items
  return <>
    <div className="project-panel project-panel-list">
        <div className="project-panel-head">
          <Space><Button icon={<PlusOutlined />} disabled={disabled || saving} onClick={() => void edit()}>新建项目</Button><Button onClick={() => refresh()}>刷新</Button></Space>
          <Input aria-label="搜索项目" placeholder="搜索项目（名称前缀）" value={keyword} maxLength={200} allowClear onChange={event => setKeyword(event.target.value)} />
          {error && <Alert type="error" title={error} action={<Button onClick={() => refresh()}>重试</Button>} />}
        </div>
        {loading && !visibleItems.length && <Spin />}
        {!loading && !error && !visibleItems.length && <p>暂无项目</p>}
        {visibleItems.length > 0 && <VirtualList className="project-list" items={visibleItems} itemKey={project => project.id} estimate={44} onEnd={loadMore} reveal={reveal && selected && selected.id === reveal.projectId ? { key: selected.id, request: reveal } : undefined} renderItem={project => <ProjectGroup key={project.id} project={project} expanded={expanded.has(project.id)} onToggle={() => toggle(project.id)} activeId={activeId} selected={selected?.id === project.id} disabled={disabled || saving || loading} reveal={reveal?.projectId === project.id ? reveal : undefined} localTarget={localTarget?.projectId === project.id ? localTarget : undefined} version={revision + refreshVersion} onSelect={() => select(project)} onConversationSelect={id => onConversationSelect?.(project, id)} onEdit={() => void edit(project)} onDelete={() => remove(project)} />} />}
        {loadingMore && <div className="project-list-more"><Spin size="small" /></div>}
    </div>
    <Modal title={editing ? '编辑项目' : '新建项目'} open={editing !== null} confirmLoading={saving} onCancel={() => { if (!saving) { setEditing(null); cancelBrowse(); cancelOperation() } }} onOk={() => void save()} okButtonProps={{ disabled: disabled || browsing || !!directoryError || !form.name.trim() || !form.path }}>
      <Space orientation="vertical" style={{ width: '100%' }}>
        <Input aria-label="项目名称" placeholder="项目名称" maxLength={200} value={form.name} onChange={event => setForm({ ...form, name: event.target.value })} />
        <Input.TextArea aria-label="项目描述" placeholder="项目描述（可选）" maxLength={2000} value={form.description} onChange={event => setForm({ ...form, description: event.target.value })} />
        <Input aria-label="项目目录" placeholder="请在下方选择主机文件夹" value={form.path} readOnly />
        <p>仅可选择程序运行用户 ~/ 内的目录，不是浏览器所在设备的目录。</p>
        <Button disabled={browsing} onClick={() => void browse()}>返回主目录</Button>
        {directoryError && <Alert type="error" title={directoryError} />}
        {browsing && <Spin />}
        {directories && <>
          <p className="project-path">{directories.path}</p>
          <Space><Button disabled={!directories.parent} onClick={() => void browse(directories.parent)}>上一级</Button><Button onClick={() => setForm({ ...form, path: directories.path })}>使用当前目录</Button></Space>
          <div className="project-directories">{directories.items.map(item => <Button block type="text" key={item.path} icon={<FolderOutlined />} onClick={() => void browse(item.path)}>{item.name}</Button>)}</div>
        </>}
      </Space>
    </Modal>
  </>
}
