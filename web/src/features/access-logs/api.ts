import { get } from '../../api/http'
import type {
  AccessLogPageData,
  AccessLogPageResponse,
  AccessLogQuery,
} from './types'

const ACCESS_LOG_PAGE_PATH = '/v1/system/accesslog/page'

export async function fetchAccessLogs(
  query: AccessLogQuery,
  signal?: AbortSignal,
): Promise<AccessLogPageData> {
  const response = await get<AccessLogPageResponse>(
    ACCESS_LOG_PAGE_PATH,
    {
      access_ip: query.accessIP,
      start_time: query.startTime,
      end_time: query.endTime,
      page: query.page,
      page_size: query.pageSize,
    },
    signal,
  )

  return {
    total: response?.total ?? 0,
    items: response?.items ?? [],
    page: response?.page ?? query.page,
    pageSize: response?.page_size ?? query.pageSize,
  }
}
