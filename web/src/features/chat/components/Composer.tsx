import { Button, Input } from 'antd'
import { SendOutlined, StopOutlined } from '@ant-design/icons'

interface Props {
  value: string
  onChange: (value: string) => void
  streaming: boolean
  disabled: boolean
  onSend: () => void
  onStop: () => void
}

export function Composer({ value, onChange, streaming, disabled, onSend, onStop }: Props) {
  return (
    <div className="chat-composer-shell">
      <div className="chat-composer">
        <Input.TextArea
          aria-label="对话消息"
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
          <span className="chat-muted">
            {streaming ? '正在通过 SSE 接收回复' : '默认模型 · Enter 发送 / Shift + Enter 换行'}
          </span>
          {streaming ? (
            <Button danger icon={<StopOutlined />} onClick={onStop}>停止</Button>
          ) : (
            <Button type="primary" aria-label="发送消息" icon={<SendOutlined />} disabled={!value.trim() || disabled} onClick={onSend} />
          )}
        </div>
      </div>
      <p className="chat-composer-hint">AI 生成的内容可能有误，请核实重要信息。</p>
    </div>
  )
}
