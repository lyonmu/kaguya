import { useState } from 'react'
import { Button, Input } from 'antd'
import { AlignLeftOutlined } from '@ant-design/icons'

interface Props {
  id?: string
  value?: string
  onChange?: (value: string) => void
  label: string
  kind: string
  parse: (value: string | undefined) => unknown
  disabled?: boolean
  placeholder?: string
}

export function MCPJSONEditor({ id, value, onChange, label, kind, parse, disabled, placeholder }: Props) {
  const [error, setError] = useState('')
  const format = () => {
    try {
      onChange?.(JSON.stringify(parse(value), null, 2))
      setError('')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'JSON 格式无效')
    }
  }
  return <div>
    <div className="overflow-hidden rounded-lg border border-k-border focus-within:border-blue-400">
      <div className="flex items-center justify-between border-b border-k-border bg-k-surface px-3 py-1">
        <span className="text-xs text-k-text-muted">JSON · {kind}</span>
        <Button type="text" size="small" icon={<AlignLeftOutlined />} disabled={disabled} aria-label={`格式化${label}`} onClick={format}>格式化</Button>
      </div>
      <Input.TextArea id={id} aria-label={label} aria-invalid={!!error} aria-describedby={error && id ? `${id}-error` : undefined}
        value={value} onChange={event => { setError(''); onChange?.(event.target.value) }} disabled={disabled}
        variant="borderless" autoSize={{ minRows: 4, maxRows: 12 }} autoComplete="off" spellCheck={false}
        placeholder={placeholder} style={{ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', fontSize: 13, lineHeight: 1.7, padding: '10px 12px' }} />
    </div>
    {error && <div id={id ? `${id}-error` : undefined} role="alert" className="mt-1 text-sm text-red-500">{error}</div>}
  </div>
}
