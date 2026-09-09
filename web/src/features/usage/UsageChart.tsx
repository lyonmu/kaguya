import { useEffect, useRef } from 'react'
import { init, use as registerCharts, type EChartsCoreOption } from 'echarts/core'
import { BarChart, HeatmapChart } from 'echarts/charts'
import { AriaComponent, CalendarComponent, GridComponent, LegendComponent, TooltipComponent, VisualMapComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

registerCharts([BarChart, HeatmapChart, AriaComponent, CalendarComponent, GridComponent, LegendComponent, TooltipComponent, VisualMapComponent, CanvasRenderer])

export function UsageChart({ option, height = 300, label }: { option: EChartsCoreOption; height?: number; label: string }) {
  const container = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!container.current) return
    const chart = init(container.current)
    chart.setOption(option)
    const observer = new ResizeObserver(() => chart.resize())
    observer.observe(container.current)
    return () => { observer.disconnect(); chart.dispose() }
  }, [option])
  return <div ref={container} role="img" aria-label={label} style={{ width: '100%', height }} />
}
