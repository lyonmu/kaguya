import { useMemo } from 'react'
import { Alert, Empty } from 'antd'
import { HIGHLIGHT_MAX_CHARS, highlightCode, langFromPath, plainLines, splitHighlightedLines } from '../highlight'
import type { FileContent } from '../types'
import { CodeLines } from './CodeLines'

export function FileViewer({ path, data }: { path: string; data: FileContent }) {
  const lines = useMemo(() => {
    const html = highlightCode(data.content, langFromPath(path))
    return html === undefined ? plainLines(data.content) : splitHighlightedLines(html)
  }, [path, data])
  if (data.binary) return <Empty className="code-empty" image={Empty.PRESENTED_IMAGE_SIMPLE} description="二进制文件暂不支持预览" />
  return (
    <>
      {data.truncated && (
        <Alert type="warning" banner message="文件超过 512KB，仅显示前 512KB 内容，已关闭语法高亮" />
      )}
      {!data.truncated && data.content.length > HIGHLIGHT_MAX_CHARS && (
        <Alert type="info" banner message="文件较大，已关闭语法高亮以保证滚动流畅" />
      )}
      <CodeLines lines={lines} />
    </>
  )
}
