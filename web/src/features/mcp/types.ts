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

export interface MCPServer extends MCPConfig {
  id: string
  enabled: boolean
  status: { state: 'running' | 'stopped' | 'error'; message?: string; tools: string[] }
  created_at: string
  updated_at: string
}

export interface MCPPage {
  items: MCPServer[]
  total: number
  page: number
  page_size: number
}
