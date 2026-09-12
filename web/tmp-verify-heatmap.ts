import * as echarts from 'echarts'
import { activityPieces } from './src/features/usage/activity'

const colors = ['#c6ddff', '#82b8ff', '#448cef', '#245aca']
const data: [string, number][] = [
  ['2025-01-01', 10],
  ['2025-01-02', 0],
  ['2025-01-03', 12],
  ['2025-01-04', 16],
  ['2025-01-05', 1000000],
]
const pieces = activityPieces(data.map(([, value]) => value), colors, String)
console.log('pieces', JSON.stringify(pieces))
const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 800, height: 240 })
chart.setOption({
  visualMap: { type: 'piecewise', show: false, pieces },
  calendar: { range: ['2025-01-01', '2025-01-31'], cellSize: ['auto', 22] },
  series: [{ type: 'heatmap', coordinateSystem: 'calendar', data }],
})
const svg = chart.renderToSVGString()
for (const color of colors) console.log(color, 'cells=', svg.split(color).length - 1)
console.log('svg length', svg.length)
