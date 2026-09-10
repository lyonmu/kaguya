import { Children, isValidElement, useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { CheckOutlined, CopyOutlined } from '@ant-design/icons'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

export function CopyButton({ text, label = '复制代码' }: { text: string; label?: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'error'>('idle')
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => () => clearTimeout(timer.current), [])
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      setState('copied')
    } catch { setState('error') }
    clearTimeout(timer.current)
    timer.current = setTimeout(() => setState('idle'), 2500)
  }
  return <button type="button" className="chat-copy" onClick={() => void copy()} aria-label={label} title={state === 'error' ? '复制失败，请选择文本后手动复制' : label}>
    {state === 'copied' ? <CheckOutlined /> : <CopyOutlined />}<span aria-live="polite">{state === 'copied' ? '已复制' : state === 'error' ? '复制失败' : '复制'}</span>
  </button>
}

export function CodeBlock({ code, language = 'text' }: { code: string; language?: string }) {
  const [highlighted, setHighlighted] = useState<{ source: string; language: string; html: string }>()
  useEffect(() => {
    if (language === 'text' || language === 'output' || code.length > 50000) return
    let active = true
    const timer = setTimeout(() => {
      void import('../highlight').then(({ highlightCode }) => {
        const html = highlightCode(code, language)
        if (active && html !== undefined) setHighlighted({ source: code, language, html })
      }).catch(() => { /* Plain text remains readable if the optional chunk cannot load. */ })
    }, 100)
    return () => { active = false; clearTimeout(timer) }
  }, [code, language])
  const html = highlighted?.source === code && highlighted.language === language ? highlighted.html : undefined
  return <div className="chat-code-block">
    <div className="chat-code-header"><span>{language}</span><CopyButton text={code} /></div>
    <pre tabIndex={0} aria-label={`${language} 代码`}><code>{html === undefined ? code : <span dangerouslySetInnerHTML={{ __html: html }} />}</code></pre>
  </div>
}

function textContent(node: ReactNode): string {
  return Children.toArray(node).map(child => {
    if (typeof child === 'string' || typeof child === 'number') return String(child)
    return isValidElement<{ children?: ReactNode }>(child) ? textContent(child.props.children) : ''
  }).join('')
}

function MarkdownImage({ src, alt }: { src?: string; alt?: string }) {
  const [approvedSource, setApprovedSource] = useState<string>()
  // Model-generated URLs may contain conversation data. Never fetch them automatically.
  if (!src) return <span>{alt}</span>
  if (approvedSource !== src) return <button type="button" className="chat-image-load" title={src} onClick={() => setApprovedSource(src)}>加载图片{alt ? `：${alt}` : ''}</button>
  return <img src={src} alt={alt ?? ''} referrerPolicy="no-referrer" />
}

export function Markdown({ text }: { text: string }) {
  return <div className="chat-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]} components={{
    pre: ({ children }) => {
      const child = Children.toArray(children)[0]
      const language = isValidElement<{ className?: string }>(child) ? child.props.className?.replace(/^language-/, '') : undefined
      return <CodeBlock code={textContent(children).replace(/\n$/, '')} language={language} />
    },
    table: ({ children }) => <div className="chat-table-scroll" tabIndex={0} aria-label="表格"><table>{children}</table></div>,
    a: ({ href, children }) => <a href={href} target="_blank" rel="noopener noreferrer">{children}</a>,
    img: MarkdownImage,
  }}>{text}</ReactMarkdown></div>
}
