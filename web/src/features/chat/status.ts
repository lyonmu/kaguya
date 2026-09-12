import type { TurnStatus } from './types'

// 服务端状态（running/completed/interrupted/canceled/failed）与本地流状态
// （streaming/done/error/stopped）在展示层合并判断，避免各处重复判断字符串。
export const isRunningStatus = (status?: TurnStatus) => status === 'streaming' || status === 'running'
export const isFailedStatus = (status?: TurnStatus) => status === 'error' || status === 'failed'
// interrupted 是断联/超时；canceled 是用户主动停止（本地未保存的 stopped 同理）。
export const isInterruptedStatus = (status?: TurnStatus) => status === 'interrupted'
export const isCanceledStatus = (status?: TurnStatus) => status === 'stopped' || status === 'canceled'
export const isIncompleteStatus = (status?: TurnStatus) => isFailedStatus(status) || isInterruptedStatus(status) || isCanceledStatus(status)
export const isCompleteStatus = (status?: TurnStatus) => status === undefined || status === 'done' || status === 'completed'
// 服务端已持久化的状态；本地临时失败轮次（error/stopped）由前端丢弃，不占用轮次索引。
export const isPersistedStatus = (status?: TurnStatus) =>
  status === undefined || status === 'running' || status === 'completed' || status === 'interrupted' || status === 'canceled' || status === 'failed'
