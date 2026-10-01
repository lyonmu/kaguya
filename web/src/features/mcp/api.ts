import { del, get, post, put } from '../../api/http'
import type { MCPConfig, MCPPage, MCPServer } from './types'

const PATH = '/v1/system/mcp'
export const fetchMCPServers = (page: number, pageSize: number, name: string, signal?: AbortSignal) =>
  get<MCPPage>(`${PATH}/page`, { page, page_size: pageSize, name }, signal)
export const fetchMCPServer = (id: string) => get<MCPServer>(`${PATH}/${id}`)
export const createMCPServer = (config: MCPConfig) => post<MCPServer>(PATH, config)
export const updateMCPServer = (id: string, config: MCPConfig) => put<MCPServer>(`${PATH}/${id}`, config)
export const setMCPEnabled = (id: string, enabled: boolean) => put<MCPServer>(`${PATH}/${id}/state`, { enabled })
// 手动重连开启新周期；熔断后仍可重试，并发请求返回忙碌。
export const reconnectMCPServer = (id: string) => post<MCPServer>(`${PATH}/${id}/reconnect`)
export const deleteMCPServer = (id: string) => del(`${PATH}/${id}`)
