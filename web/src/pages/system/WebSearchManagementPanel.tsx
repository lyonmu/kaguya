import { useEffect, useState } from 'react'
import { Alert, App, Button, Card, Form, Input, Spin, Switch, Typography } from 'antd'
import { fetchWebSearchConfig, updateWebSearchConfig } from '../../features/system-info/webSearchApi'
import type { WebSearchConfig, WebSearchPayload } from '../../features/system-info/webSearchApi'

export function WebSearchManagementPanel() {
  const { message } = App.useApp()
  const [form] = Form.useForm<WebSearchPayload>()
  const [config, setConfig] = useState<WebSearchConfig>()
  const [loadError, setLoadError] = useState('')
  const [revision, setRevision] = useState(0)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    const controller = new AbortController()
    void fetchWebSearchConfig(controller.signal).then(data => {
      if (controller.signal.aborted) return
      form.setFieldsValue({ enabled: data.enabled, api_key: '' })
      setConfig(data)
    }).catch(error => {
      if (!controller.signal.aborted) setLoadError(error instanceof Error ? error.message : '联网搜索配置加载失败')
    })
    return () => controller.abort()
  }, [form, revision])

  const save = async (values: WebSearchPayload) => {
    if (!config || saving) return
    const apiKey = values.api_key?.trim()
    if (values.enabled && !config.key_configured && !apiKey) {
      form.setFields([{ name: 'api_key', errors: ['启用前请填写 DeepSeek API Key'] }])
      return
    }
    setSaving(true)
    try {
      const data = await updateWebSearchConfig({ enabled: values.enabled, ...(apiKey ? { api_key: apiKey } : {}) })
      form.setFieldsValue({ enabled: data.enabled, api_key: '' })
      setConfig(data)
      void message.success('联网搜索配置已保存')
    } catch (error) {
      void message.error(error instanceof Error ? error.message : '联网搜索配置保存失败')
    } finally {
      setSaving(false)
    }
  }

  return <div className="mx-auto w-full max-w-[1480px] px-6 pt-4 pb-8 max-[620px]:px-3.5">
    <h2 className="mb-4 text-[22px] font-medium text-k-text">联网搜索管理</h2>
    <Card title="DeepSeek" className="max-w-[720px]">
      <Typography.Paragraph type="secondary">为支持工具调用的聊天模型提供网页搜索（web_search）和网页抓取（web_fetch）。两个接口分别按成功且有结果的请求计费。</Typography.Paragraph>
      {loadError ? <Alert type="error" title={loadError} showIcon action={<Button onClick={() => { setLoadError(''); setRevision(value => value + 1) }}>重试</Button>} /> : !config && <Spin />}
      <Form form={form} layout="vertical" onFinish={save} disabled={saving} hidden={!config}>
        <Form.Item name="enabled" label="启用联网搜索和抓取" valuePropName="checked"
          extra="开关保存后从下一轮对话生效。">
          <Switch aria-label="启用联网搜索和抓取" />
        </Form.Item>
        <Form.Item name="api_key" label="DeepSeek API Key">
          <Input.Password aria-label="DeepSeek API Key" autoComplete="new-password" maxLength={2048}
            placeholder={config?.key_configured ? '已配置，留空保留现有密钥' : '请输入 DeepSeek API Key'} />
        </Form.Item>
        <Button type="primary" htmlType="submit" loading={saving}>保存配置</Button>
      </Form>
    </Card>
  </div>
}
