import { useEffect, useState } from 'react'
import { Alert, App, Button, Card, Form, Input, InputNumber, Select, Space, Spin, Switch, Tooltip, Typography } from 'antd'
import { ReloadOutlined, SaveOutlined, SyncOutlined } from '@ant-design/icons'
import { fetchModelLabels, syncModelCatalog } from '../../features/providers/api'
import { ModelCascader } from '../../features/providers/ModelCascader'
import type { ModelLabelOption } from '../../features/providers/types'
import { fetchSystemInfo, updateSystemInfo } from '../../features/system-info/api'
import type { SystemInfo, SystemInfoPayload } from '../../features/system-info/types'

export function SystemInfoPage() {
  const { message } = App.useApp()
  const [form] = Form.useForm<SystemInfoPayload>()
  const [info, setInfo] = useState<SystemInfo>()
  const [models, setModels] = useState<ModelLabelOption[]>([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [syncing, setSyncing] = useState(false)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError('')
    Promise.all([fetchSystemInfo(controller.signal), fetchModelLabels(controller.signal)]).then(([config, labels]) => {
      if (controller.signal.aborted) return
      setInfo(config)
      setModels(labels ?? [])
      form.setFieldsValue(config)
    }).catch(error => {
      if (!controller.signal.aborted) setError(error instanceof Error ? error.message : '配置加载失败')
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [form, revision])

  const save = async (values: SystemInfoPayload) => {
    setSaving(true)
    try {
      const config = await updateSystemInfo({
        context_compaction_percent: values.context_compaction_percent,
        agent_max_steps: values.agent_max_steps, command_timeout_seconds: values.command_timeout_seconds, chat_max_retries: values.chat_max_retries,
        global_agents_paths: values.global_agents_paths ?? [],
        global_system_prompt: values.global_system_prompt ?? '', system_prompt: values.system_prompt ?? '',
        model_sync_enabled: values.model_sync_enabled, model_sync_url: values.model_sync_url, model_sync_interval_hours: values.model_sync_interval_hours,
        default_model_id: values.default_model_id || '', task_model_id: values.task_model_id || '',
      })
      setInfo(config)
      form.setFieldsValue(config)
      void message.success('系统配置已保存，新请求立即生效')
    } catch (error) { void message.error(error instanceof Error ? error.message : '保存失败') }
    finally { setSaving(false) }
  }
  const syncNow = async () => {
    setSyncing(true)
    try {
      const result = await syncModelCatalog()
      void message.success(`已同步 ${result.provider_count.toLocaleString()} 个提供商、${result.count.toLocaleString()} 个模型`)
      setRevision(value => value + 1)
    } catch (error) { void message.error(error instanceof Error ? error.message : '同步失败') }
    finally { setSyncing(false) }
  }
  const formatTime = (value?: string) => value ? new Date(value).toLocaleString() : '尚未同步'
  return <div className="mx-auto w-full max-w-[1180px] px-6 py-5 max-[620px]:px-3.5">
    <div className="mb-4 flex items-center justify-between gap-3">
      <h2 className="m-0 text-[20px] font-semibold text-k-text">系统配置</h2>
      <Button icon={<ReloadOutlined />} disabled={saving} loading={loading} onClick={() => setRevision(value => value + 1)}>重新加载</Button>
    </div>
    {error && <Alert type="error" title={error} showIcon className="mb-3" />}
    {loading ? <div className="p-6 text-center"><Spin /></div> : info && <Card styles={{ body: { padding: 18 } }}>
      <Form form={form} layout="vertical" requiredMark={false} disabled={saving || !!error} onFinish={save} size="middle">
        <div className="grid grid-cols-2 gap-x-3 max-[620px]:grid-cols-1">
          <Form.Item className="mb-3!" label="默认对话模型" name="default_model_id" tooltip="用于未手动选择模型的对话，清空则取消配置。"><ModelCascader aria-label="默认对话模型" models={models} /></Form.Item>
          <Form.Item className="mb-3!" label="后台任务模型" name="task_model_id" tooltip="用于生成对话标题等后台任务，可与对话模型相同，清空则取消配置。"><ModelCascader aria-label="后台任务模型" models={models} /></Form.Item>
        </div>
        <div className="grid grid-cols-4 gap-x-3 max-[900px]:grid-cols-2 max-[620px]:grid-cols-1">
          <Form.Item className="mb-3!" label="Agent Loop 最大步数" name="agent_max_steps" rules={[{ required: true }]} tooltip="0 表示不限制（默认，与 pi 一致）。设置上限时，到达后保存进度并暂停，可继续执行。"><InputNumber className="w-full" min={0} max={1000} /></Form.Item>
          <Form.Item className="mb-3!" label="命令超时（秒）" name="command_timeout_seconds" rules={[{ required: true }]} tooltip="每个 bash 命令的默认和最大超时，默认 120 秒。模型可请求更短时间。"><InputNumber className="w-full" min={1} max={86400} /></Form.Item>
          <Form.Item className="mb-3!" label="聊天最大重试次数" name="chat_max_retries" rules={[{ required: true }]} tooltip="模型服务临时不可用时指数退避重试；已输出内容后不会重试。"><InputNumber className="w-full" min={0} max={20} precision={0} /></Form.Item>
          <Form.Item className="mb-3!" label="会话压缩比例（%）" name="context_compaction_percent" tooltip="达到阈值后压缩早期内容，可设 10–95%。" rules={[{ required: true }]}><InputNumber className="w-full" aria-label="会话压缩比例" min={10} max={95} precision={0} /></Form.Item>
        </div>
        <Form.Item className="mb-3!" label="全局 AGENTS.md 路径" name="global_agents_paths" tooltip="会话首轮读取并保存快照，后续复用；路径修改仅影响新会话。"><Select mode="tags" placeholder="输入绝对路径或 ~/ 路径后按回车，可添加多个" /></Form.Item>
        <div className="mb-3 rounded-lg border border-k-border-soft bg-k-canvas p-3">
          <Form.Item className="mb-3!" label="目录同步地址" name="model_sync_url" rules={[{ required: true, whitespace: true, message: '请输入同步地址' }, { type: 'url', message: '请输入有效的 URL' }, { pattern: /^https?:\/\//, message: '仅支持 HTTP(S) URL' }]} tooltip="手动与定时同步均使用此地址，会同时更新提供商目录与模型目录；修改后请先保存配置。">
            <Input aria-label="目录同步地址" maxLength={2048} placeholder="https://models.dev/api.json" />
          </Form.Item>
          <div className="grid grid-cols-[minmax(160px,0.5fr)_minmax(180px,0.6fr)_1fr_auto] items-end gap-3 max-[800px]:grid-cols-2 max-[620px]:grid-cols-1">
            <Form.Item className="mb-0!" label="定时同步目录" name="model_sync_enabled" valuePropName="checked"><Switch /></Form.Item>
            <Form.Item className="mb-0!" label="同步间隔（小时）" name="model_sync_interval_hours" rules={[{ required: true }]}><InputNumber className="w-full" min={1} max={720} precision={0} /></Form.Item>
            <div className="pb-1 text-xs text-k-text-muted">
              <div>提供商 {info.provider_catalog_count.toLocaleString()} 个 · 模型 {info.model_sync_catalog_count.toLocaleString()} 项 · 最近成功：{formatTime(info.model_sync_last_success_at)}</div>
              {info.model_sync_last_error ? <Typography.Text type="danger">最近失败：{info.model_sync_last_error}</Typography.Text> : null}
            </div>
            <Button icon={<SyncOutlined />} loading={syncing} onClick={syncNow}>立即同步</Button>
          </div>
        </div>
        <Form.Item className="mb-3!" label="全局基础提示词" name="global_system_prompt" tooltip="所有聊天使用的基础人设，可直接修改；留空表示不注入基础人设。">
          <Input.TextArea aria-label="全局基础提示词" rows={6} maxLength={20000} showCount />
        </Form.Item>
        <Form.Item className="mb-3!" label="附加系统提示词" name="system_prompt" tooltip="追加到基础提示词之后；留空则只使用基础提示词。">
          <Input.TextArea aria-label="附加系统提示词" rows={4} maxLength={20000} showCount />
        </Form.Item>
        <Space><Tooltip title="保存后新发起的聊天和后台任务立即生效，无需重启。"><Button htmlType="submit" type="primary" icon={<SaveOutlined />} loading={saving}>保存配置</Button></Tooltip></Space>
      </Form>
    </Card>}
  </div>
}
