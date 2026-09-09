import { useEffect, useRef, useState } from 'react'
import { Alert, App, Button, Input, Modal, Pagination, Space, Spin } from 'antd'
import { FolderOutlined, PlusOutlined } from '@ant-design/icons'
import { deleteProject, fetchDirectories, fetchProject, fetchProjects, saveProject } from './api'
import type { Directories, Project, ProjectInput } from './api'

import { ProjectGroup } from './ProjectGroup'

const empty: ProjectInput = { name: '', path: '', description: '' }
const errorText = (error: unknown) => error instanceof Error ? error.message : '项目操作失败'

export function ProjectPanel({ selected, disabled, onSelect, activeId, onConversationSelect, refreshVersion = 0 }: { selected?: Project; disabled: boolean; onSelect: (project?: Project) => void; activeId?: string; onConversationSelect?: (project: Project, id: string) => void; refreshVersion?: number }) {
  const { message, modal } = App.useApp()
  const [keyword, setKeyword] = useState('')
  const [page, setPage] = useState(1)
  const [items, setItems] = useState<Project[]>([])
  const [total, setTotal] = useState(0)
  const [version, setVersion] = useState(0)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState<string | null>(null)
  const [form, setForm] = useState<ProjectInput>(empty)
  const [saving, setSaving] = useState(false)
  const [directories, setDirectories] = useState<Directories>()
  const [browsing, setBrowsing] = useState(false)
  const [directoryError, setDirectoryError] = useState('')
  const directoryRequest = useRef<AbortController | null>(null)
  useEffect(() => () => directoryRequest.current?.abort(), [])
  useEffect(() => {
    const controller = new AbortController()
    const timer = setTimeout(() => {
      setLoading(true)
      setError('')
      void fetchProjects(keyword, page, controller.signal).then(result => {
        if (!controller.signal.aborted) { setItems(result.items); setTotal(result.total) }
      }).catch(error => { if (!controller.signal.aborted) setError(errorText(error)) })
        .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    }, 250)
    return () => { clearTimeout(timer); controller.abort() }
  }, [keyword, page, version])
  const browse = async (path = '', selectDirectory = true) => {
    directoryRequest.current?.abort()
    const controller = new AbortController()
    directoryRequest.current = controller
    setBrowsing(true)
    setDirectoryError('')
    setDirectories(undefined)
    try {
      const result = await fetchDirectories(path, controller.signal)
      if (!controller.signal.aborted) {
        setDirectories(result)
        if (selectDirectory) setForm(current => ({ ...current, path: result.path, name: !current.name || current.name === current.path.split('/').filter(Boolean).at(-1) ? result.path.split('/').filter(Boolean).at(-1) || '' : current.name }))
      }
    } catch (error) { if (!controller.signal.aborted) setDirectoryError(errorText(error)) }
    finally { if (!controller.signal.aborted) setBrowsing(false) }
  }
  const edit = async (project?: Project) => {
    setSaving(true)
    try {
      const detail = project ? await fetchProject(project.id) : undefined
      setForm(detail ?? empty)
      setEditing(detail?.id ?? '')
      void browse('', !detail) // Editing keeps the saved path until the user navigates.
    } catch (error) { void message.error(errorText(error)) }
    finally { setSaving(false) }
  }
  const save = async () => {
    setSaving(true)
    try {
      const project = await saveProject(editing ?? '', { ...form, name: form.name.trim() })
      setEditing(null)
      setVersion(value => value + 1)
      onSelect(project)
    } catch (error) { void message.error(errorText(error)) }
    finally { setSaving(false) }
  }
  const remove = (project: Project) => modal.confirm({
    title: `删除项目“${project.name}”？`, content: '仅删除项目记录；对话解除项目归属并保留在普通对话列表，主机文件不会删除。', okText: '删除', okButtonProps: { danger: true },
    onOk: async () => {
      setSaving(true)
      try { await deleteProject(project.id); if (selected?.id === project.id) onSelect(undefined); setPage(1); setVersion(value => value + 1) }
      catch (error) { void message.error(errorText(error)); throw error }
      finally { setSaving(false) }
    },
  })
  return <>
    <div className="project-panel project-panel-list">
        <Space><Button icon={<PlusOutlined />} disabled={disabled || saving} onClick={() => void edit()}>新建项目</Button><Button onClick={() => setVersion(value => value + 1)}>刷新</Button></Space>
        <Input aria-label="搜索项目" placeholder="搜索项目（名称前缀）" value={keyword} maxLength={200} allowClear onChange={event => { setKeyword(event.target.value); setPage(1) }} />
        {error && <Alert type="error" title={error} action={<Button onClick={() => setVersion(value => value + 1)}>重试</Button>} />}
        {loading && <Spin />}
        {!loading && !error && !items.length && <p>暂无项目</p>}
        {items.map(project => <ProjectGroup key={project.id} project={project} activeId={activeId} selected={selected?.id === project.id} disabled={disabled || saving || loading} version={version + refreshVersion} onSelect={() => onSelect(project)} onConversationSelect={id => onConversationSelect?.(project, id)} onEdit={() => void edit(project)} onDelete={() => remove(project)} />)}
        <Pagination simple current={page} total={total} pageSize={20} hideOnSinglePage showSizeChanger={false} onChange={setPage} />
    </div>
    <Modal title={editing ? '编辑项目' : '新建项目'} open={editing !== null} confirmLoading={saving} onCancel={() => { if (!saving) { setEditing(null); directoryRequest.current?.abort() } }} onOk={() => void save()} okButtonProps={{ disabled: disabled || browsing || !!directoryError || !form.name.trim() || !form.path }}>
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
