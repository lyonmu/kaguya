import { useLayoutEffect, useRef } from 'react'
import { Alert, Button, Empty, Spin } from 'antd'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { Block, Turn } from '../types'

export function ContentBlock({ block }: { block: Block }) {
  if (block.type === 'text') return <div className="chat-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]}>{block.text ?? ''}</ReactMarkdown></div>
  if (block.type === 'reasoning') return <details className="chat-tool"><summary>思考过程 {block.phase === 'delta' ? '· 思考中' : ''}</summary><div className="chat-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]}>{block.text ?? ''}</ReactMarkdown></div></details>
  return <details className="chat-tool">
    <summary><span>⌘ {block.tool_name || '工具调用'}</span><span className={block.is_error || block.output?.type === 'error' ? 'chat-failure' : 'chat-muted'}>{block.is_error || block.output?.type === 'error' ? '失败' : block.output ? '已完成' : block.phase ? '执行中' : '已结束'}</span></summary>
    <div className="chat-tool-body"><h4>输入</h4><pre>{block.input || '—'}</pre>
      {block.output && <><h4>输出</h4><pre>{block.output.type === 'media' ? `媒体输出 · ${block.output.media_type || '未知类型'}（不自动加载）` : block.output.text || '（空输出）'}</pre></>}
      {block.error_message && <p className="chat-failure">{block.error_message}</p>}
    </div>
  </details>
}

interface Props {
  turns: Turn[]
  loading: boolean
  hasMore: boolean
  streaming: boolean
  onLoadMore: () => Promise<void>
}

export function MessageList({ turns, loading, hasMore, streaming, onLoadMore }: Props) {
  const viewport = useRef<HTMLDivElement>(null)
  const stick = useRef(true)
  const last = turns.at(-1)
  useLayoutEffect(() => {
    if (stick.current && viewport.current) viewport.current.scrollTop = viewport.current.scrollHeight
  }, [last])
  const loadMore = async () => {
    const element = viewport.current
    const height = element?.scrollHeight ?? 0
    stick.current = false
    await onLoadMore()
    requestAnimationFrame(() => {
      if (element) element.scrollTop += element.scrollHeight - height
    })
  }
  return <div className="chat-messages" ref={viewport} onScroll={() => {
    const el = viewport.current
    if (el) stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 100
  }}>
    <div className="chat-message-wrap">
      {hasMore && <div className="chat-center"><Button type="text" loading={loading} disabled={streaming} onClick={() => void loadMore()}>加载更早的对话</Button></div>}
      {loading && !turns.length && <div className="chat-center"><Spin /></div>}
      {!loading && !turns.length && <div className="chat-welcome"><div className="chat-welcome-logo">K</div><h1>今天，我们一起探索什么？</h1><p>向 Kaguya 提问，查看思考过程与工具执行结果。</p><Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="输入消息，开始新的对话" /></div>}
      {turns.map(turn => <section key={turn.turn_index}>
        <article className="chat-message"><div className="chat-avatar">你</div><div className="chat-message-body"><div className="chat-message-name">你 · {new Date(turn.started_at).toLocaleString()}</div><div className="chat-user-text">{turn.user_content}</div></div></article>
        <article className="chat-message"><div className="chat-avatar ai">K</div><div className="chat-message-body"><div className="chat-message-name">Kaguya {turn.model_name && `· ${turn.model_name}`}</div>
          {turn.blocks.map((block, index) => <ContentBlock key={index} block={block} />)}
          {turn.status === 'streaming' && <div className="chat-muted" role="status"><Spin size="small" /> 正在生成…</div>}
          {turn.error && <Alert type={turn.status === 'stopped' ? 'warning' : 'error'} title={turn.error} showIcon />}
          {turn.usage && <div className="chat-turn-meta">{turn.usage.total_tokens.toLocaleString()} tokens · {(turn.duration_ms / 1000).toFixed(1)} s · {turn.tool_calls} 次工具调用</div>}
        </div></article>
      </section>)}
    </div>
  </div>
}
