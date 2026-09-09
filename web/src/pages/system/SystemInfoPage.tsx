import { useEffect, useState } from 'react'
import { Alert, App, Button, Card, Form, Input, Space, Spin, Tooltip } from 'antd'
import { ReloadOutlined, SaveOutlined } from '@ant-design/icons'
import { fetchModelLabels } from '../../features/providers/api'
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
  return <div className="mx-auto w-full max-w-[1000px] px-8 py-7 max-[620px]:px-3.5">
    <div className="mb-5 flex items-center justify-between gap-3">
      <h2 className="m-0 text-[22px] font-semibold text-k-text">系统配置</h2>
      <Button icon={<ReloadOutlined />} disabled={saving} loading={loading} onClick={() => setRevision(value => value + 1)}>重新加载</Button>
    </div>
    {error && <Alert type="error" title={error} showIcon className="mb-4" />}
    {loading ? <div className="p-8 text-center"><Spin /></div> : info && <Card>
      <Form form={form} layout="vertical" requiredMark={false} disabled={saving || !!error} onFinish={save}>
        <div className="grid grid-cols-2 gap-x-4 max-[620px]:grid-cols-1">
          <Form.Item label="默认对话模型" name="default_model_id" tooltip="用于未手动选择模型的对话，清空则取消配置。"><ModelCascader aria-label="默认对话模型" models={models} /></Form.Item>
          <Form.Item label="后台任务模型" name="task_model_id" tooltip="用于生成对话标题等后台任务，可与对话模型相同，清空则取消配置。"><ModelCascader aria-label="后台任务模型" models={models} /></Form.Item>
        </div>
        <Form.Item label="User-Agent" name="user_agent" tooltip="用于服务端的聊天和标题生成请求，不修改浏览器请求头。" rules={[{ required: true, whitespace: true, message: '请输入 User-Agent' }, { pattern: /^[\x20-\x7e]+$/, message: '只能使用可打印 ASCII 字符，不能包含换行' }]}>
          <Input aria-label="User-Agent" maxLength={512} placeholder="kaguya" />
        </Form.Item>
        <Form.Item label="全局基础提示词（只读）"><div className="whitespace-pre-wrap rounded-lg bg-k-canvas p-3 text-sm text-k-text-muted">{info.global_system_prompt}</div></Form.Item>
        <Form.Item label="自定义系统提示词" name="system_prompt" tooltip="追加到基础提示词之后，仅用于聊天；留空则只使用基础提示词。">
          <Input.TextArea aria-label="自定义系统提示词" rows={7} maxLength={20000} showCount />
        </Form.Item>
        <Space><Tooltip title="保存后新发起的聊天和后台任务立即生效，无需重启。"><Button htmlType="submit" type="primary" icon={<SaveOutlined />} loading={saving}>保存配置</Button></Tooltip></Space>
      </Form>
    </Card>}
  </div>
}
