import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import { Tree } from 'antd'
import type { GitFile, ProjectNode } from '../types'

const STATUS_TEXT: Record<string, string> = {
  modified: 'M',
  added: 'A',
  deleted: 'D',
  renamed: 'R',
  copied: 'C',
  untracked: 'U',
  conflicted: '!',
}

const STATUS_NAME: Record<string, string> = {
  modified: '已修改',
  added: '已添加',
  deleted: '已删除',
  renamed: '已重命名',
  copied: '已复制',
  untracked: '未跟踪',
  conflicted: '有冲突',
}

export function StatusBadge({ status }: { status?: GitFile }) {
  if (!status) return null
  return <span className={`code-badge code-badge-${status.status}`} title={STATUS_NAME[status.status] ?? status.status}>{STATUS_TEXT[status.status] ?? '?'}</span>
}

interface TreeNode {
  key: string
  title: string
  name: string
  path: string
  isDir: boolean
  selectable: boolean
  children?: TreeNode[]
}

// changedOnly 时只保留有改动的文件及其父目录链。
function buildNodes(items: ProjectNode[], changed: Map<string, GitFile>, changedOnly: boolean): TreeNode[] {
  const nodes: TreeNode[] = []
  for (const item of items) {
    if (item.is_dir) {
      const children = buildNodes(item.children ?? [], changed, changedOnly)
      if (changedOnly && !children.length) continue
      nodes.push({ key: item.path, title: item.name, name: item.name, path: item.path, isDir: true, selectable: false, children })
    } else {
      if (changedOnly && !changed.has(item.path)) continue
      nodes.push({ key: item.path, title: item.name, name: item.name, path: item.path, isDir: false, selectable: true })
    }
  }
  return nodes
}

export function FileTree({ items, changed, selected, changedOnly, onSelect }: {
  items: ProjectNode[]
  changed: Map<string, GitFile>
  selected?: string
  changedOnly: boolean
  onSelect: (path: string) => void
}) {
  const container = useRef<HTMLDivElement>(null)
  const [height, setHeight] = useState(320)
  useLayoutEffect(() => {
    const element = container.current
    if (!element) return
    const update = () => setHeight(element.clientHeight)
    const observer = new ResizeObserver(update)
    observer.observe(element)
    update()
    return () => observer.disconnect()
  }, [])
  const nodes = useMemo(() => buildNodes(items, changed, changedOnly), [items, changed, changedOnly])
  return (
    <div ref={container} className="code-tree">
      <Tree
        key={changedOnly ? 'changed' : 'all'}
        treeData={nodes}
        height={height}
        virtual
        blockNode
        defaultExpandAll
        selectedKeys={selected ? [selected] : []}
        titleRender={node => {
          const item = node as unknown as TreeNode
          return <span className="code-tree-title"><span className="code-tree-name">{item.name}</span><StatusBadge status={changed.get(item.path)} /></span>
        }}
        onSelect={(_keys, info) => {
          const item = info.node as unknown as TreeNode
          if (!item.isDir) onSelect(item.path)
        }}
      />
    </div>
  )
}
