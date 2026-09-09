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
  start: string
  end: string
  total_tokens: number
  conversations: number
  peak_tokens: number
  peak_tokens_date: string
  peak_conversations: number
  peak_conversations_date: string
  days: UsageDay[]
  models: UsageComposition[]
  providers: UsageComposition[]
}
// startTime/endTime 为秒级 Unix 时间戳，首尾秒包含；省略时使用服务端默认范围。
export function fetchTokenUsage(startTime?: number, endTime?: number, signal?: AbortSignal) {
  return get<TokenUsage>('/v1/system/usage', { start_time: startTime, end_time: endTime }, signal)
}
