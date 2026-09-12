import { get, put } from '../../api/http'
import type { SystemInfo, SystemInfoPayload } from './types'

const PATH = '/v1/system/info'

export function fetchSystemInfo(signal?: AbortSignal) {
  return get<SystemInfo>(PATH, undefined, signal)
}

export function updateSystemInfo(payload: SystemInfoPayload) {
  return put<SystemInfo>(PATH, payload)
}
