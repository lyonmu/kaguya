export type MCPTransport = 'stdio' | 'streamable-http' | 'sse'

export interface MCPConfig {
  name: string
  transport: MCPTransport
  command: string
  args: string[]
  env: Record<string, string>
  working_directory: string
  url: string
  headers: Record<string, string>
  timeout_seconds: number
}

export type MCPPhase = 'stopped' | 'connecting' | 'running' | 'backoff' | 'circuit_open'

export interface MCPServer extends MCPConfig {
  id: string
  enabled: boolean
  // state 保留旧语义 running/stopped/error；phase 表达连接/退避/熔断细粒度状态。
  status: { state: 'running' | 'stopped' | 'error'; message?: string; tools: string[] }
  phase?: MCPPhase
  consecutive_failures?: number
  reconnect_cycle?: number
  next_retry_at?: string
  circuit_opened_at?: string
  created_at: string
  updated_at: string
}

export interface MCPPage {
  items: MCPServer[]
  total: number
  page: number
  page_size: number
}
