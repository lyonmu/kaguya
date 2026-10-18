import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Select, Tag, Tooltip } from 'antd'
import { ModelCascader } from '../../providers/ModelCascader'
import { useModelLabels } from '../../providers/useModelLabels'
import type { ModelLabelOption } from '../../providers/types'
import { FileTextOutlined, FolderOpenOutlined, SafetyCertificateOutlined, SendOutlined, StopOutlined } from '@ant-design/icons'
import { ComposerPrimitive, useAui } from '@assistant-ui/react'
import { activeMention } from '../mentions'
import { useReferences } from '../useReferences'
import { ContextProgress } from './ContextProgress'
import type { PermissionMode, Reference } from '../types'

interface Props {
  projectId?: string
  conversationId?: string
  turnCount?: number
  modelId: string
  onModelChange: (value: string) => void
  value: string
  onChange: (value: string) => void
  references: Reference[]
  onReferencesChange: (value: Reference[]) => void
  permissionMode: PermissionMode
  onPermissionChange: (value: PermissionMode) => void
  streaming: boolean
  disabled: boolean
}

const permissionOptions = [
  { value: 'full_approval', label: '完全审批', title: '每个工具调用都需要你逐个批准' },
  { value: 'auto_approval', label: '自主审批', title: '可信只读工具自动放行，写入与命令仍需批准' },
  { value: 'full_access', label: '完全访问', title: '所有工具直接执行，不逐一审批（兼容默认）' },
]

export function Composer({ projectId, conversationId, turnCount, modelId, onModelChange, value, onChange, references, onReferencesChange, permissionMode, onPermissionChange, streaming, disabled }: Props) {
  const runtime = useAui()
  useEffect(() => { runtime.composer.setText(value) }, [runtime, value])
  const input = useRef<HTMLTextAreaElement>(null)
  const mentionPopover = useRef<HTMLDivElement>(null)
  const retainsMentionFocus = (target: EventTarget | null) => target instanceof Node && (target === input.current || mentionPopover.current?.contains(target))
  const [caret, setCaret] = useState(0)
  const [dismissed, setDismissed] = useState(false)
  const [selected, setSelected] = useState(0)
  const mention = projectId && !dismissed && !streaming && !disabled ? activeMention(value, caret) : undefined
  const query = mention?.query
  useEffect(() => { setDismissed(false); setCaret(0) }, [projectId, conversationId])
  const { items: candidates, loading: filesLoading, loadingMore, error: filesError, truncated, scanIncomplete, nextCursor, loadMore } = useReferences(projectId, query, !!mention)
  useEffect(() => { setSelected(0) }, [query, projectId])
  const choices = candidates.length + (nextCursor ? 1 : 0)
  const selectedIndex = Math.min(selected, Math.max(0, choices - 1))
  const chooseReference = (candidate: { type: 'file' | 'directory'; path: string }) => {
    if (!mention) return
    if (!references.some(item => item.type === candidate.type && item.path === candidate.path)) {
      onReferencesChange([...references, { type: candidate.type, path: candidate.path }])
    }
    const token = `@${candidate.path}`
    const tail = value.slice(mention.end)
    const separator = tail === '' || !/^\s/.test(tail) ? ' ' : ''
    const nextValue = value.slice(0, mention.start) + token + separator + tail
    const nextCaret = mention.start + token.length + separator.length
    onChange(nextValue)
    setDismissed(true)
    requestAnimationFrame(() => {
      const textarea = input.current
      textarea?.focus()
      textarea?.setSelectionRange(nextCaret, nextCaret)
      setCaret(nextCaret)
    })
  }
  const { models, loading, error, retry } = useModelLabels()
  return (
    <div className="chat-composer-shell">
      <ComposerPrimitive.Root className="chat-composer" onSubmit={event => { if (disabled || streaming) event.preventDefault() }}>
        {error && <Alert type="error" title={error} action={<Button size="small" onClick={retry}>重试</Button>} />}
        {references.length > 0 && <div className="chat-reference-chips" aria-label="已选择的引用">
          {references.map(reference => <Tag key={`${reference.type}:${reference.path}`} closable onClose={() => onReferencesChange(references.filter(item => !(item.type === reference.type && item.path === reference.path)))} icon={reference.type === 'directory' ? <FolderOpenOutlined /> : <FileTextOutlined />}>
            {reference.type === 'directory' ? '目录 · ' : ''}{reference.path}
          </Tag>)}
        </div>}
        {mention && <div className="chat-mention-popover" ref={mentionPopover}>
          <div className="chat-mention-heading"><span>引用项目文件或目录</span><span>↑↓ 选择 · Enter 插入或加载 · Esc 关闭</span></div>
          {filesLoading ? <div className="chat-mention-empty" role="status">正在搜索文件…</div> : !candidates.length ? !filesError && <div className="chat-mention-empty" role="status">没有匹配文件，试试文件名或相对路径</div> : <div role="listbox" id="project-file-mentions" aria-label="项目文件">
            {candidates.map((candidate, index) => <button type="button" role="option" aria-selected={selectedIndex === index} id={`file-mention-${index}`} tabIndex={-1} key={`${candidate.type}:${candidate.path}`} onMouseDown={event => event.preventDefault()} onClick={() => chooseReference(candidate)} className={selectedIndex === index ? 'selected' : ''}>{candidate.type === 'directory' ? <FolderOpenOutlined /> : <FileTextOutlined />}<span>{candidate.path === '.' && candidate.type === 'directory' ? '项目根目录' : candidate.path}</span></button>)}
          </div>}
          {filesError && <div className="chat-mention-empty" role="alert">{filesError}</div>}
          {nextCursor && !filesLoading && <Button id="mention-load-more" type="text" size="small" loading={loadingMore} disabled={loadingMore} className={selectedIndex === candidates.length ? 'selected' : ''} onMouseDown={event => event.preventDefault()} onClick={loadMore} onBlur={event => { if (!retainsMentionFocus(event.relatedTarget)) setDismissed(true) }}>加载更多</Button>}
          {scanIncomplete && !filesLoading && <div className="chat-mention-footer">扫描未完成，请输入更具体的路径</div>}
          {truncated && !scanIncomplete && !nextCursor && !filesLoading && <div className="chat-mention-footer">结果已限制，请输入更具体的路径</div>}
        </div>}
        <div className="chat-composer-input-area">
          <ComposerPrimitive.Input
            ref={input}
            aria-controls={mention ? 'project-file-mentions' : undefined}
            aria-expanded={!!mention}
            aria-activedescendant={mention && choices ? selectedIndex === candidates.length ? 'mention-load-more' : `file-mention-${selectedIndex}` : undefined}
            aria-label="对话消息"
            className="chat-composer-input"
            placeholder={projectId ? "描述任务，输入 @ 引用项目文件或目录…" : "输入问题，与 Kaguya 对话…"}
            value={value}
            onChange={event => {
              const nextValue = event.target.value
              onChange(nextValue)
              const retained = references.filter(reference => nextValue.includes(`@${reference.path}`))
              if (retained.length !== references.length) onReferencesChange(retained)
              setCaret(event.target.selectionStart)
              setDismissed(false)
            }}
            onSelect={event => setCaret(event.currentTarget.selectionStart)}
            onBlur={event => { if (!retainsMentionFocus(event.relatedTarget)) setDismissed(true) }}
            minRows={2}
            maxRows={7}
            disabled={disabled}
            submitMode="none"
            cancelOnEscape={false}
            onKeyDown={event => {
              if (event.nativeEvent.isComposing) return
              if (mention) {
                if (event.key === 'Escape') { event.preventDefault(); setDismissed(true); return }
                if ((event.key === 'ArrowDown' || event.key === 'ArrowUp') && choices) {
                  event.preventDefault()
                  const next = (selectedIndex + (event.key === 'ArrowDown' ? 1 : -1) + choices) % choices
                  setSelected(next)
                  document.getElementById(next === candidates.length ? 'mention-load-more' : `file-mention-${next}`)?.scrollIntoView({ block: 'nearest' })
                  return
                }
                if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); if (selectedIndex === candidates.length && nextCursor) loadMore(); else if (candidates[selectedIndex]) chooseReference(candidates[selectedIndex]); return }
              }
              if (disabled || streaming) return
              if (event.key === 'Enter'  && !event.shiftKey && !event.nativeEvent.isComposing) {
                event.preventDefault()
                runtime.composer.send()
              }
            }}
          />
        </div>
        <div className="chat-composer-bottom">
          <Tooltip title={permissionOptions.find(option => option.value === permissionMode)?.title}>
            <span className={`chat-access-mode${projectId ? ' is-full' : ''}`} aria-label="会话权限模式">
              <SafetyCertificateOutlined />
              <Select
                aria-label="会话权限模式"
                size="small"
                variant="borderless"
                value={permissionMode}
                options={permissionOptions}
                onChange={value => onPermissionChange(value as PermissionMode)}
                disabled={streaming || disabled}
                popupMatchSelectWidth={false}
              />
            </span>
          </Tooltip>
          <div className="chat-composer-actions">
            <ContextProgress conversationId={conversationId} turnCount={turnCount} />
            <ModelCascader
              aria-label="对话模型"
              className="chat-model-select"
              value={modelId}
              onChange={onModelChange}
              disabled={streaming || disabled}
              loading={loading}
              models={models as ModelLabelOption[]}
              defaultOption
              displayLeafOnly
              placement="bottomRight"
            />
            {streaming ? (
              <ComposerPrimitive.Cancel className="chat-stop"><StopOutlined /> 停止</ComposerPrimitive.Cancel>
            ) : (
              <Tooltip title="Enter 发送，Shift + Enter 换行。AI 内容可能有误，请核实重要信息。">
                <span><ComposerPrimitive.Send className="chat-send" aria-label="发送消息" disabled={!value.trim() || disabled}><SendOutlined /></ComposerPrimitive.Send></span>
              </Tooltip>
            )}
          </div>
        </div>
      </ComposerPrimitive.Root>
    </div>
  )
}
