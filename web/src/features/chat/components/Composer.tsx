import { useEffect, useState } from 'react'
import { Alert, Button, Input, Select } from 'antd'
import { fetchModelLabels } from '../../providers/api'
import type { ModelLabelOption } from '../../providers/types'
import { SendOutlined, StopOutlined } from '@ant-design/icons'
import { ContextProgress } from './ContextProgress'

interface Props {
  conversationId?: string
  turnCount?: number
  modelId: string
  onModelChange: (value: string) => void
  value: string
  onChange: (value: string) => void
  streaming: boolean
  disabled: boolean
  onSend: () => void
  onStop: () => void
}

export function Composer({ conversationId, turnCount, modelId, onModelChange, value, onChange, streaming, disabled, onSend, onStop }: Props) {
  const [models, setModels] = useState<ModelLabelOption[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError('')
    fetchModelLabels(controller.signal).then(items => {
      if (!controller.signal.aborted) setModels(items ?? [])
    }).catch(error => {
      if (!controller.signal.aborted) setError(error instanceof Error ? error.message : '模型加载失败')
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [revision])
  return (
    <div className="chat-composer-shell">
      <div className="chat-composer">
        {error && <Alert type="error" title={error} action={<Button size="small" onClick={() => setRevision(value => value + 1)}>重试</Button>} />}
        <Input.TextArea
          aria-label="对话消息"
          className="chat-composer-input"
          placeholder="输入问题，与 Kaguya 对话…"
          value={value}
          onChange={event => onChange(event.target.value)}
          autoSize={{ minRows: 2, maxRows: 7 }}
          variant="borderless"
          onKeyDown={event => {
            if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
              event.preventDefault()
              onSend()
            }
          }}
        />
        <div className="chat-composer-bottom">
          <Select
            aria-label="对话模型"
            className="chat-model-select"
            value={modelId}
            onChange={onModelChange}
            disabled={streaming || disabled}
            loading={loading}
            showSearch={{ optionFilterProp: 'label' }}
            options={[{ label: '默认模型', value: '' }, ...models.map(model => ({
              label: `${model.provider_name} / ${model.label}`, value: model.value,
            }))]}
          />
          <div className="chat-composer-actions">
          <ContextProgress conversationId={conversationId} turnCount={turnCount} />
          {streaming ? (
            <Button danger icon={<StopOutlined />} onClick={onStop}>停止</Button>
          ) : (
            <Button type="primary" aria-label="发送消息" icon={<SendOutlined />} disabled={!value.trim() || disabled} onClick={onSend} />
          )}
          </div>
        </div>
      </div>
      <p className="chat-composer-hint">Enter 发送 / Shift + Enter 换行 · AI 生成的内容可能有误，请核实重要信息。</p>
    </div>
  )
}
