import type { UsageDay } from './api'

export type ActivityMode = 'daily' | 'weekly' | 'monthly'
// UTC 分桶，周一为一周起点；跨年周不能拆成两份。
export function aggregateActivity(days: UsageDay[], mode: ActivityMode): [string, number][] {
  const values = new Map<string, number>()
  for (const day of days) {
    let key = day.date
    if (mode === 'monthly') key = key.slice(0, 7)
    if (mode === 'weekly') {
      const date = new Date(`${day.date}T00:00:00Z`)
      date.setUTCDate(date.getUTCDate() - (date.getUTCDay() + 6) % 7)
      key = date.toISOString().slice(0, 10)
    }
    values.set(key, (values.get(key) ?? 0) + day.total_tokens)
  }
  return [...values.entries()].sort(([a], [b]) => a.localeCompare(b))
}

// 热力图档位：值为 0 的日期固定透明，非零数值按分位切分。
// 峰值极高时平均分档会把绝大多数日期压进最低档（甚至与空白日无法区分），
// 按分位切分能保证有使用的日期彼此可辨。
// 使用 gte/lte/value 而非 min/max：ECharts 对 min/max 的区间开闭另有兼容规则。
export interface ActivityPiece {
  value?: number
  gte?: number
  lte?: number
  color: string
  label: string
}

// activityPieces 按分位把非零用量切成最多 colors.length 档，颜色由浅到深。
// format 用于档位标签，与图表提示保持一致。
export function activityPieces(values: number[], colors: string[], format: (value: number) => string): ActivityPiece[] {
  const pieces: ActivityPiece[] = [{ value: 0, color: 'transparent', label: '0' }]
  const sorted = values.filter(value => value > 0).sort((a, b) => a - b)
  if (!sorted.length) return pieces
  const levels = Math.min(colors.length, sorted.length)
  const bounds = [1]
  for (let level = 1; level < levels; level++) {
    const boundary = sorted[Math.floor(level * sorted.length / levels)]
    if (boundary > bounds[bounds.length - 1]) bounds.push(boundary)
  }
  bounds.forEach((min, index) => {
    const max = bounds[index + 1] === undefined ? undefined : bounds[index + 1] - 1
    pieces.push(max === undefined
      ? { gte: min, color: colors[index], label: `≥ ${format(min)}` }
      : { gte: min, lte: max, color: colors[index], label: `${format(min)} - ${format(max)}` })
  })
  return pieces
}
