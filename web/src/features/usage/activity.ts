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
