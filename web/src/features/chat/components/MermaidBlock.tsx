import { useEffect, useState } from 'react'
import { CodeBlock } from './Markdown'

export function MermaidBlock({ code, streaming = false }: { code: string; streaming?: boolean }) {
  const [result, setResult] = useState<{ code: string; src?: string; error?: string }>()
  const [fullSize, setFullSize] = useState(false)
  useEffect(() => {
    if (streaming) return
    let active = true
    void import('../mermaid').then(({ renderDiagram }) => renderDiagram(code)).then(src => {
      if (active) setResult({ code, src })
    }).catch(error => {
      if (active) setResult({ code, error: error instanceof Error ? error.message : '图表渲染失败' })
    })
    return () => { active = false }
  }, [code, streaming])
  const current = result?.code === code ? result : undefined
  return <div className="chat-mermaid">
    {current?.src ? <><button className="chat-mermaid-toggle" aria-pressed={fullSize} onClick={() => setFullSize(value => !value)}>{fullSize ? '适应宽度' : '原尺寸查看'}</button><div className={`chat-mermaid-preview${fullSize ? ' full-size' : ''}`}><img src={current.src} alt="Mermaid 图表" /></div></>
      : <p role="status">{current?.error ? '图表无法渲染，请检查 Mermaid 源码。' : streaming ? '图表生成中…' : '正在渲染图表…'}</p>}
    <details open={current?.error ? true : undefined}>
      <summary>Mermaid 源码</summary>
      {current?.error && <p className="chat-failure">{current.error}</p>}
      <CodeBlock code={code} language="mermaid" />
    </details>
  </div>
}
