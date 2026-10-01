import { useEffect, useState } from 'react'
import { App } from 'antd'
import { createMCPServer, deleteMCPServer, fetchMCPServers, reconnectMCPServer, setMCPEnabled, updateMCPServer } from './api'
import type { MCPConfig, MCPPage, MCPServer } from './types'

export interface MCPQuery {
  page: number
  pageSize: number
  name: string
}

/**
 * useMCPServers 拥有 MCP 列表、可见性轮询与 CRUD 阶段状态。页面保留表单、
 * 详情抽屉等局部 UI；轮询仅在页面可见时进行，串行化加载避免堆积。
 */
export function useMCPServers() {
  const { message } = App.useApp()
  const [query, setQuery] = useState<MCPQuery>({ page: 1, pageSize: 10, name: '' })
  const [data, setData] = useState<MCPPage>({ items: [], total: 0, page: 1, page_size: 10 })
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  const [busy, setBusy] = useState('')

  useEffect(() => {
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    let inFlight = false
    let refreshPending = false
    const visible = () => document.visibilityState !== 'hidden'
    const schedule = () => {
      clearTimeout(timer)
      if (visible()) timer = setTimeout(() => void load(false), 5000)
    }
    const load = async (initial: boolean) => {
      if (controller.signal.aborted) return
      if (inFlight) {
        refreshPending = true
        return
      }
      inFlight = true
      refreshPending = false
      if (initial) setLoading(true)
      try {
        const page = await fetchMCPServers(query.page, query.pageSize, query.name, controller.signal)
        if (!controller.signal.aborted) { setData(page); setError('') }
      } catch (err) {
        if (!controller.signal.aborted) setError(err instanceof Error ? err.message : 'MCP 列表加载失败')
      } finally {
        inFlight = false
        if (!controller.signal.aborted) {
          setLoading(false)
          if (refreshPending && visible()) {
            refreshPending = false
            void load(false)
          } else {
            schedule()
          }
        }
      }
    }
    const onVisibilityChange = () => {
      clearTimeout(timer)
      if (!visible()) return
      if (inFlight) refreshPending = true
      else void load(false)
    }
    document.addEventListener('visibilitychange', onVisibilityChange)
    void load(true)
    return () => {
      controller.abort()
      clearTimeout(timer)
      document.removeEventListener('visibilitychange', onVisibilityChange)
    }
  }, [query, revision])

  const reload = () => setRevision(value => value + 1)

  const save = async (config: MCPConfig, editingID?: string) => {
    setBusy(editingID ?? 'new')
    try {
      if (editingID) await updateMCPServer(editingID, config)
      else await createMCPServer(config)
      void message.success(editingID ? 'MCP 配置已更新' : 'MCP 已创建，可通过开关启用')
      reload()
    } finally {
      setBusy('')
    }
  }

  const toggle = async (server: MCPServer, enabled: boolean) => {
    setBusy(server.id)
    try {
      await setMCPEnabled(server.id, enabled)
      void message.success(enabled ? 'MCP 已启用，新对话轮次可使用工具' : 'MCP 已停用')
    } catch (err) {
      void message.error(err instanceof Error ? err.message : 'MCP 启停失败')
    } finally {
      setBusy('')
      reload()
    }
  }

  const reconnect = async (server: MCPServer) => {
    setBusy(server.id)
    try {
      await reconnectMCPServer(server.id)
      void message.success('已开启新的重连周期')
    } catch (err) {
      void message.error(err instanceof Error ? err.message : '重连失败')
    } finally {
      setBusy('')
      reload()
    }
  }

  const remove = async (server: MCPServer) => {
    setBusy(server.id)
    try {
      await deleteMCPServer(server.id)
      void message.success('MCP 已删除')
      if (data.items.length === 1 && query.page > 1) setQuery(current => ({ ...current, page: current.page - 1 }))
      else reload()
    } catch (err) {
      void message.error(err instanceof Error ? err.message : '删除失败')
    } finally {
      setBusy('')
    }
  }

  return { query, setQuery, data, loading, error, busy, setBusy, reload, save, toggle, reconnect, remove }
}
