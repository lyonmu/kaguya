import { get, put } from '../../api/http'
import type { SystemInfo, SystemInfoPayload } from './types'

const PATH = '/v1/system/info'

export function fetchSystemInfo(signal?: AbortSignal) {
  return get<SystemInfo>(PATH, undefined, signal)
}

export function updateSystemInfo(payload: SystemInfoPayload) {
  return put<SystemInfo>(PATH, payload)
}

// 路径列表使用专用端点：不受通用 1 MiB 请求体限制，保存后立即重建监听。
export function updateAgentsPaths(paths: string[]) {
  return put<SystemInfo>(`${PATH}/agents-paths`, { paths })
}
