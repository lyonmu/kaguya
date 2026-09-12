import { get } from '../../api/http'

export interface UsageDay { date: string; total_tokens: number; conversations: number }
export interface UsageComposition {
  id: string
  name: string
  provider_id: string
  provider_name: string
  input_tokens: number
  output_tokens: number
  reasoning_tokens: number
  cached_tokens: number
  total_tokens: number
}
export interface TokenUsage {
  // start/end 与 total_tokens、conversations、peak_* 只对应上面的日期选择器范围。
  start: string
  end: string
  // activity_start/activity_end 与 days 是固定的最近一年窗口，不随日期选择器变化。
  activity_start: string
  activity_end: string
  total_tokens: number
  conversations: number
  peak_tokens: number
  peak_tokens_date: string
  peak_conversations: number
  peak_conversations_date: string
  days: UsageDay[]
  // models/providers 固定统计全部历史，与所选日期无关。
  models: UsageComposition[]
  providers: UsageComposition[]
}
// startTime/endTime 为秒级 Unix 时间戳，首尾秒包含，只影响汇总卡片与 start/end；省略时使用服务端默认范围（最近一年）。
export function fetchTokenUsage(startTime?: number, endTime?: number, signal?: AbortSignal) {
  return get<TokenUsage>('/v1/system/usage', { start_time: startTime, end_time: endTime }, signal)
}
