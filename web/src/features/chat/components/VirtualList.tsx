import { useLayoutEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'

interface Props<T> {
  items: T[]
  itemKey: (item: T) => string | number
  renderItem: (item: T) => ReactNode
  estimate: number
  className?: string
  onEnd?: () => void
  onBoundary?: (direction: -1 | 1) => void
  initialEnd?: boolean
  followEnd?: boolean
}

// 可变高度窗口化：只挂载可见项和少量预渲染项，ResizeObserver 跟踪 Markdown/工具展开后的高度。
export function VirtualList<T>({ items, itemKey, renderItem, estimate, className, onEnd, onBoundary, initialEnd = false, followEnd = false }: Props<T>) {
  const viewport = useRef<HTMLDivElement>(null)
  const heights = useRef(new Map<string | number, number>())
  const [range, setRange] = useState({ top: 0, height: 800 })
  const [, measure] = useState(0)
  const initialized = useRef(false)
  const stick = useRef(initialEnd)
  const boundaryTime = useRef(0)
  const touchY = useRef(0)
  const wasFollowing = useRef(false)
  const boundary = (direction: -1 | 1) => {
    const el = viewport.current
    if (!el || Date.now() - boundaryTime.current < 500) return
    if ((direction < 0 && el.scrollTop <= 0) || (direction > 0 && el.scrollTop + el.clientHeight >= el.scrollHeight - 1)) {
      boundaryTime.current = Date.now()
      onBoundary?.(direction)
    }
  }
  let total = 0
  const offsets = items.map(item => {
    const top = total
    total += heights.current.get(itemKey(item)) ?? estimate
    return top
  })
  let start = offsets.findIndex((top, index) => top + (heights.current.get(itemKey(items[index])) ?? estimate) >= range.top)
  if (start < 0) start = Math.max(0, items.length - 1)
  start = Math.max(0, start - 2)
  let end = start
  while (end < items.length && offsets[end] < range.top + range.height) end++
  end = Math.min(items.length, end + 3)

  useLayoutEffect(() => {
    const element = viewport.current
    if (!element) return
    const update = () => setRange({ top: element.scrollTop, height: element.clientHeight })
    const observer = new ResizeObserver(update)
    observer.observe(element)
    update()
    return () => observer.disconnect()
  }, [])
  useLayoutEffect(() => {
    const element = viewport.current
    if (!element || !items.length) return
    if (followEnd && !wasFollowing.current) stick.current = true
    wasFollowing.current = followEnd
    if ((!initialized.current && initialEnd) || ((followEnd || initialEnd) && stick.current)) {
      element.scrollTop = element.scrollHeight
      setRange({ top: element.scrollTop, height: element.clientHeight })
    }
    initialized.current = true
  }, [items, total, initialEnd, followEnd])
  useLayoutEffect(() => {
    if (items.length && range.top + range.height >= total - estimate * 2) onEnd?.()
  }, [range, total, estimate, items.length, onEnd])

  return <div ref={viewport} className={className} tabIndex={0} style={{ overflow: 'auto', overflowAnchor: 'none', minHeight: 0 }} onScroll={event => {
    const el = event.currentTarget
    stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 100
    setRange({ top: el.scrollTop, height: el.clientHeight })
  }} onWheel={event => {
    if (event.deltaY !== 0) boundary(event.deltaY < 0 ? -1 : 1)
  }} onTouchStart={event => { touchY.current = event.touches[0].clientY }} onTouchEnd={event => {
    const delta = touchY.current - event.changedTouches[0].clientY
    if (Math.abs(delta) > 30) boundary(delta < 0 ? -1 : 1)
  }} onKeyDown={event => {
    if (event.target !== event.currentTarget) return
    if (event.key === 'PageUp' || event.key === 'ArrowUp') boundary(-1)
    if (event.key === 'PageDown' || event.key === 'ArrowDown') boundary(1)
  }}>
    <div style={{ height: total, position: 'relative' }}>
      {items.slice(start, end).map((item, index) => <MeasuredRow key={itemKey(item)} top={offsets[start + index]} onHeight={height => {
        const key = itemKey(item)
        const previous = heights.current.get(key) ?? estimate
        if (height === previous) return
        heights.current.set(key, height)
        const el = viewport.current
        if (el && offsets[start + index] < el.scrollTop && !stick.current) {
          el.scrollTop += height - previous
          setRange({ top: el.scrollTop, height: el.clientHeight })
        }
        measure(value => value + 1)
      }}>{renderItem(item)}</MeasuredRow>)}
    </div>
  </div>
}

function MeasuredRow({ top, onHeight, children }: { top: number; onHeight: (height: number) => void; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => {
    const el = ref.current!
    const observer = new ResizeObserver(() => onHeight(el.getBoundingClientRect().height))
    observer.observe(el)
    onHeight(el.getBoundingClientRect().height)
    return () => observer.disconnect()
  }, [onHeight])
  return <div ref={ref} style={{ position: 'absolute', top, width: '100%', display: 'flow-root' }}>{children}</div>
}
