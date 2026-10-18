import { useState } from 'react'
import { Alert, Button, Tag } from 'antd'
import { CheckOutlined, CloseOutlined, DownOutlined, SafetyCertificateOutlined } from '@ant-design/icons'
import type { Approval } from '../types'

const effectLabel = (effect: string) => {
  if (effect === 'read_only') return { text: '只读', tone: 'success' }
  if (effect === 'mutating') return { text: '写入', tone: 'warning' }
  return { text: '未知副作用', tone: 'danger' }
}

const modeLabel = (mode: string) => {
  if (mode === 'full_approval') return '完全审批'
  if (mode === 'auto_approval') return '自主审批'
  if (mode === 'full_access') return '完全访问'
  return mode
}

const statusLabel = (approval: Approval) => {
  switch (approval.status) {
    case 'pending': return '等待批准'
    case 'approved': return '已批准'
    case 'consumed':
      switch (approval.execution_state) {
        case 'succeeded': return '工具返回成功'
        case 'failed': return '执行失败，可能已有副作用'
        case 'canceled': return approval.execution_reason_code === 'canceled_before_execution' ? '执行前已取消' : '执行已取消，可能已有副作用'
        default: return '已授权，执行结果未知'
      }
    case 'rejected': return '已拒绝'
    case 'expired': return '已超时'
    case 'canceled': return '已取消'
    default: return '审批状态未知'
  }
}

interface Props {
  approval: Approval
  onDecide?: (approvalId: string, decision: 'approve' | 'reject') => Promise<void>
}

/**
 * ApprovalCard 展示一次工具审批：工具、副作用与最终参数，一次裁决只授权当前调用。
 * 参数作为不可信数据展示，不渲染为可点击内容。
 */
export function ApprovalCard({ approval, onDecide }: Props) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const pending = approval.status === 'pending'
  const effect = effectLabel(approval.effect)
  const decide = async (decision: 'approve' | 'reject') => {
    if (!onDecide || busy) return
    setBusy(true)
    setError('')
    try {
      await onDecide(approval.approval_id, decision)
    } catch (err) {
      setError(err instanceof Error ? err.message : '裁决失败，请刷新后重试')
    } finally {
      setBusy(false)
    }
  }
  let input = approval.input || ''
  try { input = JSON.stringify(JSON.parse(input), null, 2) } catch { /* 保留不完整或非 JSON 参数原文。 */ }
  const metadata = <div className="chat-approval-meta">
    <Tag className={`lunar-tag lunar-tag-${effect.tone}`}>{effect.text}</Tag>
    <span>{modeLabel(approval.mode)}</span>
  </div>
  const parameters = input && <div className="chat-approval-parameters">
    <span className="chat-approval-label">调用参数</span>
    <pre className="chat-approval-input">{input}</pre>
  </div>
  if (!pending) return (
    <details className="chat-approval chat-approval-record">
      <summary>
        <SafetyCertificateOutlined className="chat-approval-icon" />
        <strong>{approval.tool_name || 'MCP 工具'}</strong>
        <span className="chat-approval-status">{statusLabel(approval)}</span>
        <DownOutlined className="chat-approval-chevron" />
      </summary>
      <div className="chat-approval-record-body">{metadata}{parameters}</div>
    </details>
  )
  return (
    <section className="chat-approval is-pending" aria-label={`工具审批 ${approval.tool_name || 'MCP 工具'}`}>
      <div className="chat-approval-head">
        <span className="chat-approval-icon"><SafetyCertificateOutlined /></span>
        <div className="chat-approval-heading"><strong>工具调用需要批准</strong><span>{approval.tool_name || 'MCP 工具'}</span></div>
        <span className="chat-approval-status" role="status">{statusLabel(approval)}</span>
      </div>
      <div className="chat-approval-body">
        {metadata}
        <p className="chat-approval-hint">批准只对这一次调用生效；未批准不会执行该工具。</p>
        {parameters}
        {error && <Alert type="error" showIcon title={error} />}
      </div>
      {onDecide && (
        <div className="chat-approval-actions">
          <Button icon={<CloseOutlined />} disabled={busy} onClick={() => void decide('reject')}>拒绝</Button>
          <Button type="primary" icon={<CheckOutlined />} loading={busy} onClick={() => void decide('approve')}>批准一次</Button>
        </div>
      )}
    </section>
  )
}
