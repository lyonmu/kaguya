import { useEffect, useState } from 'react'
import { Button, Progress, Spin, Tooltip } from 'antd'
import { fetchConversationContext } from '../api'
import type { ConversationContext } from '../types'

export function ContextProgress({ conversationId, turnCount }: { conversationId?: string; turnCount?: number }) {
  const [data, setData] = useState<ConversationContext>()
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [revision, setRevision] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    setData(undefined)
    setError('')
    setLoading(!!conversationId)
    if (conversationId) {
      fetchConversationContext(conversationId, controller.signal).then(result => {
        if (!controller.signal.aborted) setData(result)
      }).catch(error => {
        if (!controller.signal.aborted) setError(error instanceof Error ? error.message : '上下文占用加载失败')
      }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    }
    return () => controller.abort()
  }, [conversationId, turnCount, revision])
  const percent = data?.percent
  const known = percent != null
  const title = loading ? '正在加载上下文占用…' : error ? <>{error} <Button size="small" onClick={() => setRevision(value => value + 1)}>重试</Button></> : !conversationId ? '首轮对话结束后显示上下文占用' : <>
    <div>{data?.model_name || '最后一轮模型'} · 当前上下文占用</div>
    <div>{data?.context_tokens?.toLocaleString() ?? '未知'} / {data?.effective_window.toLocaleString() ?? '未知'} tokens</div>
    <div>有效窗口为模型最大上下文的 {Math.round((data?.window_ratio ?? 0.9) * 100)}%（其余预留给输出）</div>
    <div>{known ? `占有效窗口 ${percent.toFixed(1)}%，占最大窗口 ${data?.max_window_percent?.toFixed(1)}%` : '历史记录、模型窗口或供应商用量缺失，暂无法计算'}</div>
    <div>使用最近一次模型调用的输入（含缓存）和输出估算占用；达到系统配置的压缩比例时，下次模型调用前自动压缩早期内容，保留近期消息和原始历史。</div>
  </>
  return <Tooltip title={title}>
    <span className="chat-context-progress" tabIndex={0} aria-label={known ? `上下文占用 ${percent.toFixed(1)}%` : '上下文占用未知'}>
      {loading ? <Spin size="small" /> : <Progress type="circle" size={28} strokeWidth={10} percent={Math.min(percent ?? 0, 100)} status={known && percent >= 100 ? 'exception' : 'normal'} strokeColor={known && percent >= 100 ? '#ff4d4f' : known && percent >= 80 ? '#faad14' : undefined} format={() => known ? `${Math.round(percent)}%` : '—'} />}
    </span>
  </Tooltip>
}
