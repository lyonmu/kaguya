import mermaid from 'mermaid'

mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', suppressErrorRendering: true })
let nextDiagram = 0

export async function renderDiagram(code: string): Promise<string> {
  const container = document.createElement('div')
  container.style.cssText = 'position:fixed;left:-100000px;top:0;visibility:hidden'
  document.body.append(container)
  try {
    const { svg } = await mermaid.render(`kaguya-diagram-${++nextDiagram}`, code, container)
    // Mermaid serializes HTML labels with void tags such as <br>. SVG images
    // require XML, including self-closing tags inside foreignObject labels.
    const parsed = new DOMParser().parseFromString(svg, 'text/html')
    const element = parsed.querySelector('svg')
    if (!element) throw new Error('Mermaid 未生成有效的 SVG 图表')
    const bounds = element.getAttribute('viewBox')?.trim().split(/[\s,]+/).map(Number)
    if (bounds?.length === 4 && bounds.every(Number.isFinite) && bounds[2] > 0 && bounds[3] > 0) {
      element.setAttribute('width', String(bounds[2]))
      element.setAttribute('height', String(bounds[3]))
    }
    const image = new XMLSerializer().serializeToString(element)
    // An image document isolates the generated SVG from the chat DOM and disables scripts.
    return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(image)}`
  } finally { container.remove() }
}
