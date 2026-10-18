import { useEffect, useRef, useState } from 'react'
import { App } from 'antd'
import type { FormInstance } from 'antd'
import { fetchSystemInfo, updateAgentsPaths, updateSystemInfo } from './api'
import type { InstructionStatus, SystemInfo, SystemInfoPayload } from './types'

/**
 * useSystemInfo 管理系统配置的加载、保存与目录同步状态。表单实例由页面持有：
 * 加载成功后写入表单；同步只刷新状态字段，不覆盖未保存输入。
 */
export function useSystemInfo(form: FormInstance<SystemInfoPayload>) {
  const { message } = App.useApp()
  const [info, setInfo] = useState<SystemInfo>()
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [syncing, setSyncing] = useState(false)
  const [error, setError] = useState('')
  const [instructionStatusError, setInstructionStatusError] = useState(false)
  const [revision, setRevision] = useState(0)
  const syncRequest = useRef<AbortController | null>(null)
  useEffect(() => () => { syncRequest.current?.abort() }, [])

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError('')
    fetchSystemInfo(controller.signal).then(config => {
      if (controller.signal.aborted) return
      setInfo(config)
      setInstructionStatusError(false)
      form.setFieldsValue(config)
    }).catch(err => {
      if (!controller.signal.aborted) setError(err instanceof Error ? err.message : '配置加载失败')
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [form, revision])

  // 只轮询发布状态，不能用状态响应覆盖用户尚未保存的表单。
  const hasInfo = !!info
  useEffect(() => {
    if (!hasInfo) return
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout>
    const refresh = async () => {
      try {
        const config = await fetchSystemInfo(controller.signal)
        if (!controller.signal.aborted) {
          setInfo(current => current && { ...current, instruction_status: mergeInstructionStatus(current.instruction_status, config.instruction_status) })
          setInstructionStatusError(false)
        }
      } catch {
        if (!controller.signal.aborted) setInstructionStatusError(true)
      } finally {
        if (!controller.signal.aborted) timer = setTimeout(() => void refresh(), 2000)
      }
    }
    timer = setTimeout(() => void refresh(), 2000)
    return () => { controller.abort(); clearTimeout(timer) }
  }, [hasInfo, revision])

  const reload = () => setRevision(value => value + 1)

  const save = async (values: SystemInfoPayload) => {
    setSaving(true)
    try {
      await updateSystemInfo({
        context_compaction_percent: values.context_compaction_percent,
        agent_max_steps: values.agent_max_steps, command_timeout_seconds: values.command_timeout_seconds, chat_max_retries: values.chat_max_retries,
        global_system_prompt: values.global_system_prompt ?? '', system_prompt: values.system_prompt ?? '',
        model_sync_enabled: values.model_sync_enabled, model_sync_url: values.model_sync_url, provider_sync_url: values.provider_sync_url, model_sync_interval_hours: values.model_sync_interval_hours,
        default_model_id: values.default_model_id || '', task_model_id: values.task_model_id || '',
        memory_enabled: info?.memory_enabled ?? false, memory_auto_capture: info?.memory_auto_capture ?? false,
        memory_context_tokens: info?.memory_context_tokens ?? 2000,
      })
      // 路径保存使用专用端点；返回包含最新的指令 reload 状态。
      const config = await updateAgentsPaths(values.global_agents_paths ?? [])
      setInfo(current => ({ ...config, instruction_status: mergeInstructionStatus(current?.instruction_status, config.instruction_status) }))
      form.setFieldsValue(config)
      void message.success('系统配置已保存，全局指令在后台加载')
    } catch (err) {
      void message.error(err instanceof Error ? err.message : '保存失败')
      throw err
    } finally {
      setSaving(false)
    }
  }

  const syncNow = async () => {
    if (syncRequest.current) return
    const controller = new AbortController()
    syncRequest.current = controller
    setSyncing(true)
    try {
      const { syncModelCatalog } = await import('../providers/api')
      const result = await syncModelCatalog()
      if (!controller.signal.aborted) void message.success(`已同步 ${result.provider_count.toLocaleString()} 个提供商、${result.count.toLocaleString()} 个模型`)
    } catch (err) {
      if (!controller.signal.aborted) {
        const failure = err instanceof Error ? err.message : '同步失败'
        void message.error(failure)
        setInfo(current => current && { ...current, model_sync_last_error: failure })
      }
    } finally {
      // 同步成功或失败都只刷新状态，不重新装载表单或覆盖未保存输入。
      if (!controller.signal.aborted) {
        try {
          const status = await fetchSystemInfo(controller.signal)
          if (!controller.signal.aborted) setInfo(current => current && { ...current,
            model_sync_last_error: status.model_sync_last_error,
            model_sync_last_attempt_at: status.model_sync_last_attempt_at,
            model_sync_last_success_at: status.model_sync_last_success_at,
            model_sync_catalog_count: status.model_sync_catalog_count,
            provider_catalog_count: status.provider_catalog_count,
          })
        } catch {
          if (!controller.signal.aborted) void message.error('同步状态刷新失败，请重试或重新加载')
        }
        if (!controller.signal.aborted) setSyncing(false)
      }
      if (syncRequest.current === controller) syncRequest.current = null
    }
  }

  return { info, setInfo, loading, saving, syncing, error, instructionStatusError, reload, save, syncNow }
}

// 单次应用生命周期内发布状态只前进；手动重新加载会重新建立基线。
function mergeInstructionStatus(current: InstructionStatus | undefined, next: InstructionStatus | undefined) {
  if (!current) return next
  if (!next || next.generation < current.generation || next.loaded_generation < current.loaded_generation || next.version < current.version) return current
  return next
}
