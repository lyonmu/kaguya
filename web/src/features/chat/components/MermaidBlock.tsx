import { lazy, Suspense, useEffect, useState } from 'react'
import { CodeBlock } from './Markdown'

const AntMermaid = lazy(() => import('./AntMermaid').then(module => ({ default: module.AntMermaid })))

export function MermaidBlock({ code, streaming = false }: { code: string; streaming?: boolean }) {
  const [result, setResult] = useState<{ code: string; valid?: boolean; error?: string }>()
  useEffect(() => {
    if (streaming) return
    let active = true
    void import('../mermaid').then(({ validateDiagram }) => validateDiagram(code)).then(() => {
      if (active) setResult({ code, valid: true })
    }).catch(error => {
      if (active) setResult({ code, error: error instanceof Error ? error.message : '图表渲染失败' })
    })
    return () => { active = false }
  }, [code, streaming])
  const current = result?.code === code ? result : undefined
  if (streaming || !current) return <div className="chat-mermaid-source"><CodeBlock code={code} language="mermaid" /></div>
  return <div className="chat-mermaid">
    {current.valid ? <Suspense fallback={<CodeBlock code={code} language="mermaid" />}><AntMermaid code={code} /></Suspense>
      : <><p className="chat-failure">图表无法渲染，请检查 Mermaid 源码。</p><p className="chat-failure">{current.error}</p><CodeBlock code={code} language="mermaid" /></>}
  </div>
}
