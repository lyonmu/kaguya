import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type PointerEvent as ReactPointerEvent } from 'react'
import { Alert, Button, Drawer, Empty, Segmented, Spin, Switch, Tooltip } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { fetchFileContent, fetchFileDiff, fetchGitStatus, fetchProjectTree } from '../api'
import type { CodeBrowseView, FileContent, FileDiff, GitFile, GitStatus, ProjectNode, ProjectTree } from '../types'
import { useDiffTheme } from '../useDiffTheme'
import { DiffViewer } from './DiffViewer'
import { FileIcon } from './FileIcon'
import { FileTree, StatusBadge } from './FileTree'
import { FileViewer } from './FileViewer'
import '../code.css'

const errorText = (error: unknown) => (error instanceof Error ? error.message : '加载失败，请重试')

// 文件树侧栏宽度：可拖动调整，并按会话记住。
const SIDE_WIDTH_KEY = 'kaguya-code-side-width'
const SIDE_MIN_WIDTH = 200
const SIDE_MAX_WIDTH = 720

function initialSideWidth() {
  if (typeof window === 'undefined') return 292
  try {
    const saved = Number(window.localStorage.getItem(SIDE_WIDTH_KEY))
    if (Number.isFinite(saved) && saved >= SIDE_MIN_WIDTH) return Math.min(SIDE_MAX_WIDTH, saved)
  } catch {
    // 隐私模式等场景下读取失败时使用默认宽度
  }
  return 292
}

function formatSize(size: number) {
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  return `${(size / 1024 / 1024).toFixed(1)} MB`
}

function countFiles(nodes: ProjectNode[]): number {
  let total = 0
  for (const node of nodes) total += node.is_dir ? countFiles(node.children ?? []) : 1
  return total
}

export function CodeBrowserDrawer({ open, projectId, projectName, onClose }: {
  open: boolean
  projectId: string
  projectName?: string
  onClose: () => void
}) {
  const [tree, setTree] = useState<ProjectTree>()
  const [status, setStatus] = useState<GitStatus>()
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [version, setVersion] = useState(0)
  const [selected, setSelected] = useState<string>()
  const [view, setView] = useState<CodeBrowseView>('file')
  const [mode, setMode] = useState<'unified' | 'split'>('unified')
  const [sideWidth, setSideWidth] = useState(initialSideWidth)
  const [changedOnly, setChangedOnly] = useState(false)
  const [content, setContent] = useState<FileContent>()
  const [diff, setDiff] = useState<FileDiff>()
  const [fileLoading, setFileLoading] = useState(false)
  const [fileError, setFileError] = useState('')
  const contentCache = useRef(new Map<string, FileContent>())
  const diffCache = useRef(new Map<string, FileDiff>())
  // 缓存按项目隔离：面板打开期间切换项目时不能复用其他项目同名文件的内容。
  const cacheKey = useCallback((path: string) => `${projectId}\n${path}`, [projectId])
  const theme = useDiffTheme()

  const statusMap = useMemo(() => {
    const map = new Map<string, GitFile>()
    for (const file of status?.files ?? []) map.set(file.path, file)
    return map
  }, [status])
  const sizeMap = useMemo(() => {
    const map = new Map<string, number>()
    const walk = (nodes: ProjectNode[]) => {
      for (const node of nodes) {
        if (node.is_dir) walk(node.children ?? [])
        else map.set(node.path, node.size)
      }
    }
    walk(tree?.items ?? [])
    return map
  }, [tree])

  // 打开面板或手动刷新时并行加载文件树与 Git 状态。
  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    setLoading(true)
    setError('')
    Promise.all([fetchProjectTree(projectId, controller.signal), fetchGitStatus(projectId, controller.signal)])
      .then(([treeData, gitStatus]) => {
        if (controller.signal.aborted) return
        setTree(treeData)
        setStatus(gitStatus)
      })
      .catch(loadError => {
        if (!controller.signal.aborted) setError(errorText(loadError))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [open, projectId, version])

  // 关闭时卸载数据与缓存，避免常驻内存。
  useEffect(() => {
    if (open) return
    setTree(undefined)
    setStatus(undefined)
    setSelected(undefined)
    setContent(undefined)
    setDiff(undefined)
    setFileError('')
    setError('')
    contentCache.current.clear()
    diffCache.current.clear()
  }, [open])

  // 按当前视图加载选中文件；同一面板会话内命中缓存时不重复请求。
  useEffect(() => {
    if (!open || !selected) return
    const controller = new AbortController()
    setFileError('')
    if (view === 'diff') {
      setContent(undefined)
      const cached = diffCache.current.get(cacheKey(selected))
      if (cached) {
        setDiff(cached)
        setFileLoading(false)
        return
      }
      setDiff(undefined)
      setFileLoading(true)
      fetchFileDiff(projectId, selected, controller.signal)
        .then(data => {
          if (controller.signal.aborted) return
          diffCache.current.set(cacheKey(selected), data)
          setDiff(data)
        })
        .catch(loadError => {
          if (!controller.signal.aborted) setFileError(errorText(loadError))
        })
        .finally(() => {
          if (!controller.signal.aborted) setFileLoading(false)
        })
    } else {
      setDiff(undefined)
      const cached = contentCache.current.get(cacheKey(selected))
      if (cached) {
        setContent(cached)
        setFileLoading(false)
        return
      }
      setContent(undefined)
      setFileLoading(true)
      fetchFileContent(projectId, selected, controller.signal)
        .then(data => {
          if (controller.signal.aborted) return
          contentCache.current.set(cacheKey(selected), data)
          setContent(data)
        })
        .catch(loadError => {
          if (!controller.signal.aborted) setFileError(errorText(loadError))
        })
        .finally(() => {
          if (!controller.signal.aborted) setFileLoading(false)
        })
    }
    return () => controller.abort()
  }, [open, selected, view, projectId, version, cacheKey])

  const refresh = useCallback(() => {
    contentCache.current.clear()
    diffCache.current.clear()
    setVersion(value => value + 1)
  }, [])

  const selectFile = (path: string) => {
    setSelected(path)
    setView(statusMap.has(path) ? 'diff' : 'file')
  }

  const file = selected ? statusMap.get(selected) : undefined
  const fileSize = selected ? sizeMap.get(selected) : undefined
  const width = Math.min(1280, Math.round((typeof window === 'undefined' ? 1440 : window.innerWidth) * 0.94))
  const resizeStart = useRef<{ pointerX: number; startWidth: number } | null>(null)
  useEffect(() => {
    try {
      window.localStorage.setItem(SIDE_WIDTH_KEY, String(sideWidth))
    } catch {
      // 存储失败不影响当前会话的宽度
    }
  }, [sideWidth])
  const onResizeStart = (event: ReactPointerEvent<HTMLDivElement>) => {
    try {
      event.currentTarget.setPointerCapture(event.pointerId)
    } catch {
      // 合成事件或不支持指针捕获的环境下仍由元素上的监听处理拖动
    }
    resizeStart.current = { pointerX: event.clientX, startWidth: sideWidth }
  }
  const onResizeMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const start = resizeStart.current
    if (!start) return
    const available = typeof window === 'undefined' ? 1440 : window.innerWidth
    const upper = Math.min(SIDE_MAX_WIDTH, Math.round(available * 0.7))
    setSideWidth(Math.min(upper, Math.max(SIDE_MIN_WIDTH, start.startWidth + event.clientX - start.pointerX)))
  }
  const onResizeEnd = (event: ReactPointerEvent<HTMLDivElement>) => {
    resizeStart.current = null
    try {
      if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId)
    } catch {
      // 同上，忽略不支持指针捕获的环境
    }
  }
  return (
    <Drawer
      className="code-browser-drawer"
      size={width}
      open={open}
      onClose={onClose}
      destroyOnHidden
      title={
        <div className="code-toolbar">
          <div className="code-toolbar-title">
            <strong>{projectName || tree?.name || '项目文件'}</strong>
            {status?.is_git && <span className="code-branch">{status.branch}{status.head ? ` · ${status.head}` : ''}</span>}
          </div>
          <div className="code-toolbar-actions">
            {status?.is_git && status.files.length > 0 && (
              <label className="code-changed-only">
                <Switch size="small" checked={changedOnly} onChange={setChangedOnly} />
                只看改动
              </label>
            )}
            <Tooltip title="重新加载文件树与改动">
              <Button size="small" type="text" aria-label="重新加载代码视图" icon={<ReloadOutlined />} disabled={!open} onClick={refresh} />
            </Tooltip>
          </div>
        </div>
      }
      styles={{ body: { padding: 0, overflow: 'hidden' } }}
    >
      <div className="code-browser" style={{ '--code-side-width': `${sideWidth}px` } as CSSProperties}>
        <div className="code-browser-side">
          {error && <Alert type="error" showIcon message={error} action={<Button size="small" onClick={refresh}>重试</Button>} />}
          {tree ? (
            <FileTree items={tree.items} changed={statusMap} selected={selected} changedOnly={changedOnly} onSelect={selectFile} />
          ) : (
            loading && <div className="code-loading"><Spin size="small" /></div>
          )}
        </div>
        <div
          className="code-resizer"
          role="separator"
          aria-orientation="vertical"
          aria-label="拖动调整文件树宽度"
          onPointerDown={onResizeStart}
          onPointerMove={onResizeMove}
          onPointerUp={onResizeEnd}
          onPointerCancel={onResizeEnd}
        />
        <div className="code-browser-main">
          {!error && status && !status.is_git && <Alert type="info" showIcon message={status.message ?? '当前项目不是 Git 仓库，仅支持浏览文件'} />}
          {status?.truncated && <Alert type="warning" showIcon message="变更文件较多，仅显示前 1000 个" />}
          {tree?.truncated && <Alert type="warning" showIcon message="项目文件较多，文件树仅显示部分内容" />}
          <div className="code-view-head">
            {selected ? (
              <div className="code-view-title">
                <FileIcon path={selected} />
                <span className="code-view-path" title={selected}>{selected}</span>
                <StatusBadge status={file} />
                {file && <span className="code-view-stats">+{file.additions} −{file.deletions}</span>}
                {fileSize !== undefined && <span className="code-view-size">{formatSize(fileSize)}</span>}
              </div>
            ) : (
              <span className="code-view-path code-view-muted">未选择文件</span>
            )}
            <div className="code-view-actions">
              {selected && file && (
                <Segmented
                  size="small"
                  value={view}
                  options={[{ label: '差异', value: 'diff' }, { label: '文件', value: 'file' }]}
                  onChange={value => setView(value as CodeBrowseView)}
                />
              )}
              {selected && view === 'diff' && (
                <Segmented
                  size="small"
                  value={mode}
                  options={[{ label: '统一', value: 'unified' }, { label: '分栏', value: 'split' }]}
                  onChange={value => setMode(value as 'unified' | 'split')}
                />
              )}
            </div>
          </div>
          <div className="code-view-body">
            {fileLoading && <div className="code-loading"><Spin size="small" /></div>}
            {!fileLoading && fileError && <Alert type="error" showIcon message={fileError} />}
            {!fileLoading && !fileError && selected && view === 'diff' && diff && (
              <DiffViewer key={`diff:${projectId}:${selected}`} path={selected} diff={diff.diff} binary={diff.binary} truncated={diff.truncated} mode={mode} theme={theme} />
            )}
            {!fileLoading && !fileError && selected && view === 'file' && content && (
              <FileViewer key={`file:${projectId}:${selected}`} path={selected} data={content} />
            )}
            {!fileLoading && !fileError && !selected && (
              <Empty
                className="code-empty"
                image={Empty.PRESENTED_IMAGE_SIMPLE}
                description={tree ? `共 ${countFiles(tree.items)} 个文件${status?.is_git ? `，${status.files.length} 个改动` : ''}，选择左侧文件查看` : '正在加载项目文件'}
              />
            )}
          </div>
        </div>
      </div>
    </Drawer>
  )
}
