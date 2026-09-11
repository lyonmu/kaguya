import mermaid from 'mermaid'

mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', suppressErrorRendering: true })

export async function validateDiagram(code: string): Promise<void> {
  if (!await mermaid.parse(code, { suppressErrors: true })) throw new Error('Mermaid 源码语法无效')
}
