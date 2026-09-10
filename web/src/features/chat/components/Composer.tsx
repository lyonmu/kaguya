import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Tooltip } from 'antd'
import { ModelCascader } from '../../providers/ModelCascader'
import { fetchModelLabels } from '../../providers/api'
import type { ModelLabelOption } from '../../providers/types'
import { FileTextOutlined, SendOutlined, StopOutlined } from '@ant-design/icons'
import { ComposerPrimitive, useAui } from '@assistant-ui/react'
import { activeMention, mentionToken, referencedFiles } from '../mentions'
import { searchProjectFiles } from '../api'
import { ContextProgress } from './ContextProgress'

interface Props {
  projectId?: string
  conversationId?: string
  turnCount?: number
  modelId: string
  onModelChange: (value: string) => void
  value: string
  onChange: (value: string) => void
  streaming: boolean
  disabled: boolean
}

export function Composer({ projectId, conversationId, turnCount, modelId, onModelChange, value, onChange, streaming, disabled }: Props) {
  const runtime = useAui()
  useEffect(() => { runtime.composer.setText(value) }, [runtime, value])
  const input = useRef<HTMLTextAreaElement>(null)
  const [caret, setCaret] = useState(0)
  const [dismissed, setDismissed] = useState(false)
  const [files, setFiles] = useState<string[]>([])
  const [filesLoading, setFilesLoading] = useState(false)
  const [filesError, setFilesError] = useState('')
  const [truncated, setTruncated] = useState(false)
  const [selected, setSelected] = useState(0)
  const mention = projectId && !dismissed && !streaming && !disabled ? activeMention(value, caret) : undefined
  const query = mention?.query
  const references = projectId ? referencedFiles(value) : []
  useEffect(() => { setDismissed(false); setCaret(0) }, [projectId, conversationId])
  useEffect(() => {
    setFiles([]); setFilesError(''); setSelected(0)
    if (query === undefined || !projectId) { setFilesLoading(false); return }
    const controller = new AbortController()
    setFilesLoading(true)
    const timer = setTimeout(() => {
      searchProjectFiles(projectId, query, controller.signal).then(result => {
        if (!controller.signal.aborted) { setFiles(result.files); setTruncated(result.truncated) }
      }).catch(error => {
        if (!controller.signal.aborted) setFilesError(error instanceof Error ? error.message : '文件搜索失败')
      }).finally(() => { if (!controller.signal.aborted) setFilesLoading(false) })
    }, 150)
    return () => { clearTimeout(timer); controller.abort() }
  }, [query, projectId])
  const chooseFile = (path: string) => {
    if (!mention) return
    const token = mentionToken(path) + ' '
    onChange(value.slice(0, mention.start) + token + value.slice(mention.end))
    setDismissed(true)
    requestAnimationFrame(() => {
      const textarea = input.current
      textarea?.focus()
      textarea?.setSelectionRange(mention.start + token.length, mention.start + token.length)
      setCaret(mention.start + token.length)
    })
  }
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
      <ComposerPrimitive.Root className="chat-composer" onSubmit={event => { if (disabled || streaming || references.length > 8) event.preventDefault() }}>
        {error && <Alert type="error" title={error} action={<Button size="small" onClick={() => setRevision(value => value + 1)}>重试</Button>} />}
        {mention && <div className="chat-mention-popover">
          <div className="chat-mention-heading"><span>引用项目文件</span><span>↑↓ 选择 · Enter 插入 · Esc 关闭</span></div>
          {filesLoading ? <div className="chat-mention-empty" role="status">正在搜索文件…</div> : filesError ? <div className="chat-mention-empty" role="alert">{filesError}</div> : !files.length ? <div className="chat-mention-empty" role="status">没有匹配文件，试试文件名或相对路径</div> : <div role="listbox" id="project-file-mentions" aria-label="项目文件">
            {files.map((path, index) => <button type="button" role="option" aria-selected={selected === index} id={`file-mention-${index}`} tabIndex={-1} key={path} onMouseDown={event => event.preventDefault()} onClick={() => chooseFile(path)} className={selected === index ? 'selected' : ''}><FileTextOutlined /><span>{path}</span></button>)}
          </div>}
          {truncated && !filesLoading && <div className="chat-mention-footer">结果已限制，请输入更具体的路径</div>}
        </div>}
        <ComposerPrimitive.Input
          ref={input}
          aria-controls={mention ? 'project-file-mentions' : undefined}
          aria-expanded={!!mention}
          aria-activedescendant={mention && files.length ? `file-mention-${selected}` : undefined}
          aria-label="对话消息"
          className="chat-composer-input"
          placeholder={projectId ? "描述任务，输入 @ 引用项目文件…" : "输入问题，与 Kaguya 对话…"}
          value={value}
          onChange={event => { onChange(event.target.value); setCaret(event.target.selectionStart); setDismissed(false) }}
          onSelect={event => setCaret(event.currentTarget.selectionStart)}
          onBlur={() => setDismissed(true)}
          minRows={2}
          maxRows={7}
          disabled={disabled}
          submitMode="none"
          cancelOnEscape={false}
          onKeyDown={event => {
            if (event.nativeEvent.isComposing) return
            if (mention) {
              if (event.key === 'Escape') { event.preventDefault(); setDismissed(true); return }
              if ((event.key === 'ArrowDown' || event.key === 'ArrowUp') && files.length) {
                event.preventDefault()
                const next = (selected + (event.key === 'ArrowDown' ? 1 : -1) + files.length) % files.length
                setSelected(next)
                document.getElementById(`file-mention-${next}`)?.scrollIntoView({ block: 'nearest' })
                return
              }
              if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); if (files[selected]) chooseFile(files[selected]); return }
            }
            if (references.length > 8 || disabled || streaming) return
            if (event.key === 'Enter'  && !event.shiftKey && !event.nativeEvent.isComposing) {
              event.preventDefault()
              runtime.composer.send()
            }
          }}
        />
        {references.length > 0 && <div className="chat-reference-chips" aria-label="已引用文件">{references.map(path => <span key={path}><FileTextOutlined /><span title={path}>{path}</span></span>)}</div>}
        {references.length > 8 && <div className="chat-failure" role="alert">每次最多引用 8 个文件，请减少引用</div>}
        <div className="chat-composer-bottom">
          <ModelCascader
            aria-label="对话模型"
            className="chat-model-select"
            value={modelId}
            onChange={onModelChange}
            disabled={streaming || disabled}
            loading={loading}
            models={models}
            defaultOption
          />
          <div className="chat-composer-actions">
          <ContextProgress conversationId={conversationId} turnCount={turnCount} />
          {streaming ? (
            <ComposerPrimitive.Cancel className="chat-stop"><StopOutlined /> 停止</ComposerPrimitive.Cancel>
          ) : (
            <Tooltip title="Enter 发送，Shift + Enter 换行。AI 内容可能有误，请核实重要信息。">
              <span><ComposerPrimitive.Send className="chat-send" aria-label="发送消息" disabled={!value.trim() || disabled || references.length > 8}><SendOutlined /></ComposerPrimitive.Send></span>
            </Tooltip>
          )}
          </div>
        </div>
      </ComposerPrimitive.Root>
    </div>
  )
}
