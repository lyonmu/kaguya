import { useEffect, useState } from 'react'
import { CheckCircleOutlined, CloseCircleOutlined, CodeOutlined, FileTextOutlined, LoadingOutlined, BulbOutlined, DownOutlined } from '@ant-design/icons'
import type { Block } from '../types'
import { CodeBlock, Markdown } from './Markdown'

function useDuration(block: Block, running: boolean) {
  const [now, setNow] = useState(Date.now())
  useEffect(() => {
    if (!running) return
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [running])
  if (!block.started_at) return ''
  const end = block.finished_at ? Date.parse(block.finished_at) : running ? now : NaN
  const seconds = Math.max(0, (end - Date.parse(block.started_at)) / 1000)
  return Number.isFinite(seconds) ? `${seconds.toFixed(seconds < 10 ? 1 : 0)}s` : ''
}

function toolSummary(block: Block) {
  let input: Record<string, unknown> = {}
  try {
    const parsed: unknown = JSON.parse(block.input || '{}')
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) input = parsed as Record<string, unknown>
  } catch { /* Partial streaming JSON. */ }
  const names: Record<string, string> = { read: '读取文件', bash: '执行命令', edit: '修改文件', write: '写入文件', grep: '搜索内容', find: '查找文件', ls: '浏览目录' }
  const detail = input.command ?? input.path ?? input.query ?? input.pattern ?? ''
  return { label: names[block.tool_name || ''] || block.tool_name || '工具调用', detail: typeof detail === 'string' ? detail : '', input }
}

export function ActivityBlock({ block, streaming = false }: { block: Block; streaming?: boolean }) {
  const thinking = block.type === 'reasoning'
  const failed = !!(block.is_error || block.output?.type === 'error' || block.error_message)
  const running = streaming && !failed && (thinking ? block.phase !== 'block_end' : !block.output)
  const duration = useDuration(block, running)
  const info = toolSummary(block)
  const [open, setOpen] = useState(false)
  const state = failed ? 'failed' : running ? 'running' : !thinking && !block.output ? 'interrupted' : 'done'
  const status = { failed: '失败', running: thinking ? '思考中' : '执行中', interrupted: '未完成', done: thinking ? '思考完成' : '完成' }[state]
  const input = Object.keys(info.input).length ? JSON.stringify(info.input, null, 2) : block.input || '（无参数）'
  return <details className={`chat-activity ${thinking ? 'chat-reasoning' : 'chat-tool-card'} is-${state}`} open={open} onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>
      <span className="chat-activity-icon">{running ? <LoadingOutlined spin /> : thinking ? <BulbOutlined /> : failed ? <CloseCircleOutlined /> : block.tool_name === 'bash' ? <CodeOutlined /> : <FileTextOutlined />}</span>
      <span className="chat-activity-heading"><strong>{thinking ? '思考过程' : info.label}</strong>{!thinking && info.detail && <code title={info.detail}>{info.detail}</code>}</span>
      <span className="chat-activity-status">{state === 'done' && !thinking && <CheckCircleOutlined />}{status}</span>
      {duration && <span className="chat-activity-duration">{duration}</span>}<DownOutlined className="chat-activity-chevron" />
    </summary>
    {open && (thinking ? <div className="chat-reasoning-content"><Markdown text={block.text || '正在整理思路…'} /></div> : <div className="chat-activity-body">
      <div className="chat-activity-section">{block.tool_name === 'bash' ? '命令' : '参数'}</div>
      <CodeBlock code={block.tool_name === 'bash' && typeof info.input.command === 'string' ? info.input.command : input} language={block.tool_name === 'bash' ? 'bash' : 'json'} />
      {block.output && <><div className="chat-activity-section">输出</div><CodeBlock code={block.output.type === 'media' ? `媒体输出 · ${block.output.media_type || '未知类型'}（不自动加载）` : block.output.text || '（空输出）'} language="output" /></>}
      {block.error_message && <p className="chat-failure">{block.error_message}</p>}
    </div>)}
  </details>
}
