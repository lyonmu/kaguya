import { useMemo, useState } from 'react'
import { Button, Empty } from 'antd'
import { DiffModeEnum, DiffView } from '@git-diff-view/react'
import '@git-diff-view/react/styles/diff-view.css'

// 超过该行数的 diff 默认不渲染，避免单次产生过多 DOM 卡住面板。
const RENDER_LINE_LIMIT = 4000

export function DiffViewer({ path, diff, binary, truncated, mode, theme }: {
  path: string
  diff: string
  binary: boolean
  truncated: boolean
  mode: 'unified' | 'split'
  theme: 'light' | 'dark'
}) {
  const [expanded, setExpanded] = useState(false)
  const lineCount = useMemo(() => (diff ? diff.split('\n').length : 0), [diff])
  const data = useMemo(() => ({ newFile: { fileName: path, content: '' }, hunks: [diff] }), [path, diff])
  if (binary) return <Empty className="code-empty" image={Empty.PRESENTED_IMAGE_SIMPLE} description="二进制文件暂不支持差异预览" />
  if (!lineCount) return <Empty className="code-empty" image={Empty.PRESENTED_IMAGE_SIMPLE} description="没有可显示的差异" />
  if (lineCount > RENDER_LINE_LIMIT && !expanded) {
    return (
      <div className="code-diff-large">
        <p>该文件差异较大（约 {lineCount.toLocaleString()} 行），为避免页面卡顿暂未渲染。</p>
        <Button size="small" onClick={() => setExpanded(true)}>仍然渲染完整差异</Button>
      </div>
    )
  }
  return (
    <div className="code-diff">
      {truncated && <div className="code-diff-truncated">差异超过 1MB，内容已截断</div>}
      <DiffView
        data={data}
        diffViewMode={mode === 'split' ? DiffModeEnum.Split : DiffModeEnum.Unified}
        diffViewTheme={theme}
        diffViewHighlight
        diffViewWrap={false}
        diffViewFontSize={12}
      />
    </div>
  )
}
