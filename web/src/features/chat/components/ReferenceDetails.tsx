import { Button } from 'antd'
import type { ReferenceSummary } from '../types'

const reasons = { deadline: '扫描期限', metadata_budget: '元信息容量', context_budget: '上下文容量' }
const statuses = { included: '已包含', not_included: '未包含', empty: '空文件', binary: '二进制文件', too_large: '文件过大', oversized_line: '单行过长', read_failed: '读取失败' }
const pathLabel = (path: string) => path === '.' ? '项目根目录' : path

export function ReferenceDetails({ summary, onRestore, disabled = false }: {
  summary?: ReferenceSummary
  onRestore?: () => void
  disabled?: boolean
}) {
  if (!summary?.sources.length) return null
  const requested = summary.preparation_state === 'requested'
  const files = summary.sources.flatMap(source => source.files)
  const included = files.filter(file => file.included).length
  const partial = summary.scan_incomplete || summary.partial_reason || files.some(file => file.next_line || file.status === 'not_included' || file.status === 'read_failed')
  const caption = requested ? '仅记录选择，尚未解析' : !summary.preparation_state ? '旧记录，解析阶段未知' : partial ? included ? '部分资料已包含' : '资料未完整包含' : included ? `已包含 ${included} 个文件片段` : '没有包含文本'
  return <details className="chat-reference-details">
    <summary>引用资料 · {caption}{!requested && summary.budget_known !== true ? ' · 模型窗口未知' : ''}</summary>
    {!requested && <p>{summary.scan_incomplete ? '目录扫描未完成' : '目录扫描未标记为未完成'}{summary.partial_reason ? `；${reasons[summary.partial_reason]}导致部分包含` : ''} · 实际内联 {summary.inline_bytes.toLocaleString()} bytes · 估算 {summary.estimated_tokens.toLocaleString()} tokens</p>}
    {summary.sources.map((source, index) => <div key={`${source.type}:${source.path}:${index}`}>
      <p>{source.type === 'directory' ? '目录' : '文件'}：{pathLabel(source.path)}</p>
      {!source.files.length && (source.hash || source.ranges.length > 0) && <p>来源记录范围：{source.ranges.map(range => `${range.start}–${range.end}`).join('、') || '未记录'} · 来源摘要：<code>{source.hash || '未记录'}</code></p>}
      {source.files.map((file, fileIndex) => <div key={`${file.path}:${fileIndex}`}>
        <p>{file.path} · {statuses[file.status]}{file.resolved_path && file.resolved_path !== file.path ? ` · 实际路径 ${file.resolved_path}` : ''}</p>
        {file.included && <p>实际行：{file.ranges.map(range => `${range.start}–${range.end}`).join('、')} · 片段 SHA-256：<code>{file.hash}</code>{file.next_line ? ` · 下一行 ${file.next_line}` : ''}</p>}
      </div>)}
      {source.warnings.map((warning, warningIndex) => <p key={warningIndex}>{warning}</p>)}
    </div>)}
    {summary.warnings.map((warning, index) => <p key={index}>{warning}</p>)}
    {summary.continuation.map((item, index) => <div key={index}>
      <p>探索提示：{pathLabel(item.root)} · {reasons[item.reason]}{item.last_stable_path ? ` · 最后观察 ${item.last_stable_path}` : ''}</p>
      {item.unscanned_subtrees.length > 0 && <p>尚未完成的范围：{item.unscanned_subtrees.map(pathLabel).join('、')}</p>}
      {item.known_omitted_files > 0 && <p>已知遗漏 {item.known_omitted_files} 个文件</p>}
      <p>这是探索提示，不是目录快照。</p>
    </div>)}
    {onRestore && <Button size="small" disabled={disabled} onClick={onRestore}>恢复引用选择（{summary.sources.length} 项）</Button>}
  </details>
}
