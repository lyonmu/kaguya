export interface AccessLog {
  id: string
  access_ip: string
  access_time: number
  os?: string
  platform?: string
  browser_name?: string
  browser_version?: string
  browser_engine_name?: string
  browser_engine_version?: string
}

export interface AccessLogPageResponse {
  total?: number
  items?: AccessLog[]
  page?: number
  page_size?: number
}

export interface AccessLogPageData {
  total: number
  items: AccessLog[]
  page: number
  pageSize: number
}

export interface AccessLogQuery {
  accessIP?: string
  startTime?: number // Unix 秒，包含；0 或省略表示不限
  endTime?: number // Unix 秒，包含；0 或省略表示不限
  page: number
  pageSize: number
}
