import { useEffect, useState } from 'react'
import { Alert, App, Button, Card, Form, Input, Select, Space, Spin } from 'antd'
import { ReloadOutlined, SaveOutlined } from '@ant-design/icons'
import { fetchModelLabels } from '../../features/providers/api'
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
        system_prompt: values.system_prompt ?? '', user_agent: values.user_agent,
        default_model_id: values.default_model_id || '', task_model_id: values.task_model_id || '',
      })
      setInfo(config)
      form.setFieldsValue(config)
      void message.success('系统配置已保存，新请求立即生效')
    } catch (error) { void message.error(error instanceof Error ? error.message : '保存失败') }
    finally { setSaving(false) }
  }
  const options = models.map(model => ({ value: model.value, label: `${model.provider_name} / ${model.label}` }))
  return <div className="mx-auto w-full max-w-[1000px] px-8 py-7 max-[620px]:px-3.5">
    <div className="mb-5 flex items-center justify-between gap-3">
      <div><h2 className="m-0 text-[22px] font-semibold text-k-text">系统配置</h2><p className="mt-2 text-xs text-k-text-muted">保存后新发起的聊天和后台任务立即生效，无需重启。</p></div>
      <Button icon={<ReloadOutlined />} disabled={saving} loading={loading} onClick={() => setRevision(value => value + 1)}>重新加载</Button>
    </div>
    {error && <Alert type="error" title={error} showIcon className="mb-4" />}
    {loading ? <div className="p-8 text-center"><Spin /></div> : info && <Card>
      <Form form={form} layout="vertical" requiredMark={false} disabled={saving || !!error} onFinish={save}>
        <Alert type="info" showIcon className="mb-5" title="模型选择统一在这里管理" description="默认模型用于未手动选模型的对话；任务模型用于生成对话标题等后台任务。两者可选择同一模型，清空则取消配置。删除模型或提供商会清空相关选择。" />
        <div className="grid grid-cols-2 gap-x-4 max-[620px]:grid-cols-1">
          <Form.Item label="默认对话模型" name="default_model_id"><Select aria-label="默认对话模型" allowClear placeholder="未配置" showSearch={{ optionFilterProp: 'label' }} options={options} /></Form.Item>
          <Form.Item label="后台任务模型" name="task_model_id"><Select aria-label="后台任务模型" allowClear placeholder="未配置" showSearch={{ optionFilterProp: 'label' }} options={options} /></Form.Item>
        </div>
        <Form.Item label="User-Agent" name="user_agent" extra="用于服务端发往 LLM API 的所有请求，包括聊天和标题生成；不会修改浏览器请求头。" rules={[{ required: true, whitespace: true, message: '请输入 User-Agent' }, { pattern: /^[\x20-\x7e]+$/, message: '只能使用可打印 ASCII 字符，不能包含换行' }]}>
          <Input aria-label="User-Agent" maxLength={512} placeholder="kaguya" />
        </Form.Item>
        <Form.Item label="全局基础提示词（只读）"><div className="whitespace-pre-wrap rounded-lg bg-k-canvas p-3 text-sm text-k-text-muted">{info.global_system_prompt}</div></Form.Item>
        <Form.Item label="自定义系统提示词" name="system_prompt" extra="追加到全局基础提示词之后，用于聊天。留空时只使用基础提示词；标题生成保留专用任务提示词。">
          <Input.TextArea aria-label="自定义系统提示词" rows={7} maxLength={20000} showCount />
        </Form.Item>
        <Space><Button htmlType="submit" type="primary" icon={<SaveOutlined />} loading={saving}>保存配置</Button></Space>
      </Form>
    </Card>}
  </div>
}
