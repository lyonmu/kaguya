import type { MCPConfig, MCPTransport } from './types'

export interface MCPFormValues {
  name: string
  transport: MCPTransport
  command?: string
  args_json?: string
  env_json?: string
  working_directory?: string
  url?: string
  headers_json?: string
  timeout_seconds: number
}

function parseJSON(value: string | undefined, fallback: unknown, label: string): unknown {
  if (!value?.trim()) return fallback
  try { return JSON.parse(value) }
  catch { throw new Error(`${label}必须是有效 JSON`) }
}

export function parseStringMap(value: string | undefined, label: string): Record<string, string> {
  const parsed = parseJSON(value, {}, label)
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed) || Object.values(parsed).some(item => typeof item !== 'string')) {
    throw new Error(`${label}必须是字符串键值对象`)
  }
  return parsed as Record<string, string>
}

export function parseArgs(value: string | undefined): string[] {
  const parsed = parseJSON(value, [], '命令参数')
  if (!Array.isArray(parsed) || parsed.some(item => typeof item !== 'string')) throw new Error('命令参数必须是字符串数组')
  return parsed
}

export function toMCPConfig(values: MCPFormValues): MCPConfig {
  const stdio = values.transport === 'stdio'
  return {
    name: values.name.trim(), transport: values.transport, timeout_seconds: values.timeout_seconds,
    command: stdio ? values.command?.trim() ?? '' : '',
    args: stdio ? parseArgs(values.args_json) : [],
    env: stdio ? parseStringMap(values.env_json, '环境变量') : {},
    working_directory: stdio ? values.working_directory?.trim() ?? '' : '',
    url: stdio ? '' : values.url?.trim() ?? '',
    headers: stdio ? {} : parseStringMap(values.headers_json, '请求头'),
  }
}

export function toMCPForm(config?: MCPConfig): MCPFormValues {
  return {
    name: config?.name ?? '', transport: config?.transport ?? 'streamable-http',
    command: config?.command ?? '', working_directory: config?.working_directory ?? '',
    url: config?.url ?? '', timeout_seconds: config?.timeout_seconds ?? 60,
    args_json: JSON.stringify(config?.args ?? [], null, 2),
    env_json: JSON.stringify(config?.env ?? {}, null, 2),
    headers_json: JSON.stringify(config?.headers ?? {}, null, 2),
  }
}
