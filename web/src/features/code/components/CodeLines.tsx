import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import type { UIEvent } from 'react'

const ROW_HEIGHT = 20
const OVERSCAN = 12

// 固定行高窗口化：只挂载视口附近的代码行，避免大文件产生海量 DOM。
export function CodeLines({ lines }: { lines: string[] }) {
  const viewport = useRef<HTMLDivElement>(null)
  const frame = useRef(0)
  const [scrollTop, setScrollTop] = useState(0)
  const [height, setHeight] = useState(0)
  useLayoutEffect(() => {
    const element = viewport.current
    if (!element) return
    const update = () => setHeight(element.clientHeight)
    const observer = new ResizeObserver(update)
    observer.observe(element)
    update()
    return () => {
      observer.disconnect()
      if (frame.current) cancelAnimationFrame(frame.current)
    }
  }, [])
  useEffect(() => {
    const element = viewport.current
    if (!element) return
    element.scrollTop = 0
    setScrollTop(0)
  }, [lines])
  const onScroll = (event: UIEvent<HTMLDivElement>) => {
    const top = event.currentTarget.scrollTop
    if (frame.current) return
    frame.current = requestAnimationFrame(() => {
      frame.current = 0
      setScrollTop(top)
    })
  }
  const total = lines.length
  const start = Math.max(0, Math.floor(scrollTop / ROW_HEIGHT) - OVERSCAN)
  const end = Math.min(total, Math.ceil((scrollTop + height) / ROW_HEIGHT) + OVERSCAN)
  const digits = String(Math.max(total, 1)).length
  return (
    <div ref={viewport} className="code-lines" onScroll={onScroll} tabIndex={0}>
      <div className="code-lines-canvas" style={{ height: total * ROW_HEIGHT }}>
        <div className="code-lines-window" style={{ transform: `translateY(${start * ROW_HEIGHT}px)` }}>
          {lines.slice(start, end).map((line, offset) => (
            <div className="code-line" key={start + offset}>
              <span className="code-line-number" style={{ width: `${digits + 2}ch` }}>{start + offset + 1}</span>
              <code className="code-line-text" dangerouslySetInnerHTML={{ __html: line || ' ' }} />
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
