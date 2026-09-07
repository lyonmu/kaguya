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
  startTime?: number
  endTime?: number
  page: number
  pageSize: number
}
