import { useEffect, useState } from 'react'
import { Button, Progress, Spin, Tooltip } from 'antd'
import { fetchConversationContext } from '../api'
import type { ConversationContext } from '../types'

function compactTokens(value?: number | null) {
  if (value == null) return '未知'
  if (value < 1000) return value.toLocaleString()
  const scaled = value / 1000
  return `${scaled < 10 ? scaled.toFixed(1) : Math.round(scaled)}k`
}

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
    <div className="chat-context-tooltip">
      <span>{data?.model_name || '最后一轮模型'} · 上下文窗口</span>
      {known ? <>
        <strong>{Math.round(percent)}% 已用（剩余 {Math.max(0, 100 - Math.round(percent))}%）</strong>
        <span>已用 {compactTokens(data?.context_tokens)} tokens，共 {compactTokens(data?.effective_window)} 可用</span>
      </> : <span>历史记录或模型窗口信息不足，暂无法计算</span>}
    </div>
  </>
  return <Tooltip title={title} placement="top">
    <span className="chat-context-progress" tabIndex={0} aria-label={known ? `上下文占用 ${percent.toFixed(1)}%` : '上下文占用未知'}>
      {loading ? <Spin size="small" /> : <Progress type="circle" size={22} strokeWidth={16} percent={Math.min(percent ?? 0, 100)} status={known && percent >= 100 ? 'exception' : 'normal'} strokeColor={known && percent >= 100 ? '#ff4d4f' : known && percent >= 80 ? '#faad14' : undefined} showInfo={false} />}
    </span>
  </Tooltip>
}
