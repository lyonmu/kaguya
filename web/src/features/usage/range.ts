import dayjs from 'dayjs'

// 日期选择器只选择日历日期；显式附带 Z，避免浏览器时区将 UTC 统计范围偏移。
// 结束时间包含该日最后一秒，不使用毫秒级 valueOf()。
export function usageDateRange(start: string, end: string): [number, number] {
  return [dayjs(`${start}T00:00:00Z`).unix(), dayjs(`${end}T23:59:59Z`).unix()]
}
