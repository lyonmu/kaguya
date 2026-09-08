import { useEffect, useMemo, useState } from 'react'
import {
  ApiOutlined,
  DeleteOutlined,
  EditOutlined,
  PlusOutlined,
  ReloadOutlined,
  RobotOutlined,
  SearchOutlined,
  UndoOutlined,
} from '@ant-design/icons'
import {
  Alert,
  App,
  Button,
  Card,
  Drawer,
  Empty,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
} from 'antd'
import type { TableColumnsType } from 'antd'
import {
  createModel,
  createProvider,
  deleteModel,
  deleteProvider,
  fetchProviderLabels,
  updateModel,
  updateProvider,
} from '../../features/providers/api'
import type {
  AIModel,
  AIProvider,
  LabelOption,
  ModelPayload,
  ProviderPayload,
  ProviderProtocol,
} from '../../features/providers/types'
import {
  DEFAULT_PROVIDER_QUERY,
  useProviders,
} from '../../features/providers/useProviders'

const protocolOptions = [
  { label: 'OpenAI Chat Completions', value: 'openai-chat' },
  { label: 'Anthropic Messages', value: 'anthropic' },
  { label: 'OpenAI Responses', value: 'openai-response' },
]

const statusOptions = [
  { label: '启用', value: 1 },
  { label: '禁用', value: 2 },
]

const protocolLabel: Record<ProviderProtocol, string> = {
  'openai-chat': 'OpenAI Chat',
  anthropic: 'Anthropic',
  'openai-response': 'OpenAI Responses',
}

interface FilterValues {
  providerName?: string
  apiProtocol?: ProviderProtocol
}

function maskAPIKey(value?: string) {
  if (!value) return '未配置'
  if (value.length <= 8) return '••••••••'
  return `${value.slice(0, 4)}••••${value.slice(-4)}`
}

export function ProviderManagementPage() {
  const { message } = App.useApp()
  const [filterForm] = Form.useForm<FilterValues>()
  const [providerForm] = Form.useForm<ProviderPayload>()
  const [modelForm] = Form.useForm<ModelPayload>()
  const { data, error, loading, query, reload, setQuery } = useProviders()
  const [providerModalOpen, setProviderModalOpen] = useState(false)
  const [editingProvider, setEditingProvider] = useState<AIProvider>()
  const [providerSaving, setProviderSaving] = useState(false)
  const [modelModalOpen, setModelModalOpen] = useState(false)
  const [editingModel, setEditingModel] = useState<AIModel>()
  const [modelSaving, setModelSaving] = useState(false)
  const [selectedProviderID, setSelectedProviderID] = useState<string>()
  const [providerLabels, setProviderLabels] = useState<LabelOption[]>([])

  const selectedProvider = useMemo(
    () => data.items.find((item) => item.id === selectedProviderID),
    [data.items, selectedProviderID],
  )

  useEffect(() => {
    const controller = new AbortController()
    fetchProviderLabels(controller.signal)
      .then((options) => setProviderLabels(options ?? []))
      .catch(() => undefined)
    return () => controller.abort()
  }, [data.items])

  const showCreateProvider = () => {
    setEditingProvider(undefined)
    providerForm.setFieldsValue({
      provider_name: '',
      api_protocol: 'openai-chat',
      api_key: '',
      base_url: '',
    })
    setProviderModalOpen(true)
  }

  const showEditProvider = (provider: AIProvider) => {
    setEditingProvider(provider)
    providerForm.setFieldsValue({
      provider_name: provider.provider_name,
      api_protocol: provider.api_protocol,
      api_key: provider.api_key,
      base_url: provider.base_url,
    })
    setProviderModalOpen(true)
  }

  const saveProvider = async () => {
    try {
      const values = await providerForm.validateFields()
      setProviderSaving(true)
      if (editingProvider) {
        await updateProvider(editingProvider.id, values)
        message.success('提供商已更新')
      } else {
        await createProvider(values)
        message.success('提供商已创建')
      }
      setProviderModalOpen(false)
      reload()
    } catch (saveError) {
      if (saveError instanceof Error) message.error(saveError.message)
    } finally {
      setProviderSaving(false)
    }
  }

  const removeProvider = async (provider: AIProvider) => {
    try {
      await deleteProvider(provider.id)
      if (selectedProviderID === provider.id) setSelectedProviderID(undefined)
      message.success('提供商及其模型已删除')
      reload()
    } catch (requestError) {
      message.error(requestError instanceof Error ? requestError.message : '删除失败')
    }
  }

  const showCreateModel = () => {
    if (!selectedProvider) return
    setEditingModel(undefined)
    modelForm.setFieldsValue({
      provider_id: selectedProvider.id,
      model_name: "",
      model_id: "",
      is_default: selectedProvider.models.length === 0 ? 1 : 2,
      reasoning_enabled: 1,
      reasoning_effort: "medium",
      token_context_window: 1000000,
      token_max_output_tokens: 272000,
      capability_tool_use: 1,
      capability_vision: 1,
      capability_structured_output: 1,
    });
    setModelModalOpen(true)
  }

  const showEditModel = (model: AIModel) => {
    setEditingModel(model)
    modelForm.setFieldsValue({
      provider_id: model.provider_id,
      model_name: model.model_name,
      model_id: model.model_id,
      is_default: model.is_default,
      reasoning_enabled: model.reasoning_enabled,
      reasoning_effort: model.reasoning_effort,
      token_context_window: model.token_context_window,
      token_max_output_tokens: model.token_max_output_tokens,
      capability_tool_use: model.capability_tool_use,
      capability_vision: model.capability_vision,
      capability_structured_output: model.capability_structured_output,
    })
    setModelModalOpen(true)
  }

  const saveModel = async () => {
    try {
      const values = await modelForm.validateFields()
      setModelSaving(true)
      if (editingModel) {
        await updateModel(editingModel.id, values)
        message.success('模型已更新')
      } else {
        await createModel(values)
        message.success('模型已创建')
      }
      setModelModalOpen(false)
      reload()
    } catch (saveError) {
      if (saveError instanceof Error) message.error(saveError.message)
    } finally {
      setModelSaving(false)
    }
  }

  const removeModel = async (model: AIModel) => {
    try {
      await deleteModel(model.id)
      message.success('模型已删除')
      reload()
    } catch (requestError) {
      message.error(requestError instanceof Error ? requestError.message : '删除失败')
    }
  }

  const providerColumns: TableColumnsType<AIProvider> = [
    {
      title: '提供商',
      dataIndex: 'provider_name',
      key: 'provider_name',
      width: 210,
      render: (value: string) => (
        <span className="inline-flex items-center gap-2 font-medium text-k-text">
          <span className="grid h-7 w-7 place-items-center rounded-lg bg-k-selected text-k-primary">
            <ApiOutlined />
          </span>
          {value}
        </span>
      ),
    },
    {
      title: 'API 协议',
      dataIndex: 'api_protocol',
      key: 'api_protocol',
      width: 170,
      render: (value: ProviderProtocol) => (
        <Tag color={value === 'anthropic' ? 'orange' : 'blue'}>
          {protocolLabel[value] ?? value}
        </Tag>
      ),
    },
    {
      title: 'Base URL',
      dataIndex: 'base_url',
      key: 'base_url',
      ellipsis: true,
      render: (value: string) => (
        <Tooltip title={value || '使用协议默认地址'}>
          <span className="font-mono text-[11px] text-k-text-muted">
            {value || '默认地址'}
          </span>
        </Tooltip>
      ),
    },
    {
      title: 'API Key',
      dataIndex: 'api_key',
      key: 'api_key',
      width: 150,
      render: (value: string) => (
        <span className="font-mono text-[11px] text-k-text-muted">
          {maskAPIKey(value)}
        </span>
      ),
    },
    {
      title: '模型',
      dataIndex: 'models',
      key: 'models',
      width: 90,
      align: 'center',
      render: (models: AIModel[]) => <Tag>{models?.length ?? 0}</Tag>,
    },
    {
      title: '操作',
      key: 'actions',
      fixed: 'right',
      width: 230,
      render: (_, provider) => (
        <Space size={4}>
          <Button
            icon={<RobotOutlined />}
            onClick={() => setSelectedProviderID(provider.id)}
            size="small"
            type="link"
          >
            模型管理
          </Button>
          <Button icon={<EditOutlined />} onClick={() => showEditProvider(provider)} size="small" type="text">
            编辑
          </Button>
          <Popconfirm
            description="该提供商下的模型也会被删除。"
            okButtonProps={{ danger: true }}
            okText="删除"
            onConfirm={() => removeProvider(provider)}
            title={`确认删除 ${provider.provider_name}？`}
          >
            <Button danger icon={<DeleteOutlined />} size="small" type="text" />
          </Popconfirm>
        </Space>
      ),
    },
  ]

  const modelColumns: TableColumnsType<AIModel> = [
    {
      title: '模型名称',
      dataIndex: 'model_name',
      key: 'model_name',
      width: 180,
      render: (value: string, model) => (
        <span className="font-medium text-k-text">
          {value} {model.is_default === 1 ? <Tag color="gold">默认</Tag> : null}
        </span>
      ),
    },
    {
      title: '模型标识',
      dataIndex: 'model_id',
      key: 'model_id',
      width: 180,
      render: (value: string) => <Typography.Text copyable>{value}</Typography.Text>,
    },
    {
      title: '推理',
      key: 'reasoning',
      width: 110,
      render: (_, model) =>
        model.reasoning_enabled === 1 ? <Tag color="purple">{model.reasoning_effort}</Tag> : <Tag>关闭</Tag>,
    },
    {
      title: '上下文',
      dataIndex: 'token_context_window',
      key: 'token_context_window',
      width: 110,
      render: (value: number) => value?.toLocaleString() || '—',
    },
    {
      title: '能力',
      key: 'capabilities',
      width: 190,
      render: (_, model) => (
        <Space size={[2, 4]} wrap>
          {model.capability_tool_use === 1 ? <Tag>Tool</Tag> : null}
          {model.capability_vision === 1 ? <Tag>Vision</Tag> : null}
          {model.capability_structured_output === 1 ? <Tag>JSON</Tag> : null}
        </Space>
      ),
    },
    {
      title: '操作',
      key: 'actions',
      fixed: 'right',
      width: 120,
      render: (_, model) => (
        <Space size={2}>
          <Button icon={<EditOutlined />} onClick={() => showEditModel(model)} size="small" type="text" />
          <Popconfirm okText="删除" onConfirm={() => removeModel(model)} title={`确认删除 ${model.model_name}？`}>
            <Button danger icon={<DeleteOutlined />} size="small" type="text" />
          </Popconfirm>
        </Space>
      ),
    },
  ]

  return (
    <div className="mx-auto w-full max-w-[1480px] px-8 pt-7 pb-11 max-[900px]:px-5 max-[620px]:px-3.5 max-[620px]:pt-5">
      <div className="mb-5 flex items-end justify-between gap-6 max-[620px]:items-start">
        <div>
          <span className="mb-2 block text-[9px] font-bold tracking-[1.4px] text-k-text-subtle">SYSTEM / AI CONFIGURATION</span>
          <h2 className="m-0 text-[22px] font-semibold tracking-[-0.25px] text-k-text">AI 提供商管理</h2>
          <p className="mt-1.5 mb-0 text-xs text-k-text-muted">配置模型服务提供商、访问凭证及其可用模型。</p>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} loading={loading} onClick={reload}>刷新</Button>
          <Button icon={<PlusOutlined />} onClick={showCreateProvider} type="primary">新增提供商</Button>
        </Space>
      </div>

      <Card className="mb-3.5 border-k-border! bg-k-surface! shadow-sm shadow-black/5">
        <Form
          form={filterForm}
          layout="vertical"
          onFinish={(values) => setQuery({
            page: 1,
            pageSize: query.pageSize,
            providerName: values.providerName?.trim() || undefined,
            apiProtocol: values.apiProtocol,
          })}
          requiredMark={false}
        >
          <div className="grid grid-cols-[minmax(220px,1fr)_minmax(220px,0.7fr)_auto] items-end gap-3.5 max-[800px]:grid-cols-1">
            <Form.Item className="mb-0!" label="提供商名称" name="providerName">
              <Input allowClear placeholder="搜索提供商" prefix={<SearchOutlined className="text-k-text-subtle" />} />
            </Form.Item>
            <Form.Item className="mb-0!" label="API 协议" name="apiProtocol">
              <Select allowClear options={protocolOptions} placeholder="全部协议" />
            </Form.Item>
            <Space>
              <Button icon={<UndoOutlined />} onClick={() => {
                filterForm.resetFields()
                setQuery({ ...DEFAULT_PROVIDER_QUERY })
              }}>重置</Button>
              <Button htmlType="submit" icon={<SearchOutlined />} type="primary">查询</Button>
            </Space>
          </div>
        </Form>
      </Card>

      {error ? <Alert className="mb-3.5" message="提供商加载失败" description={error} showIcon type="error" /> : null}

      <Card className="overflow-hidden border-k-border! bg-k-surface! shadow-sm shadow-black/5" styles={{ body: { padding: 0 } }}>
        <div className="flex min-h-16 items-center justify-between border-b border-k-border-soft px-4 py-3.5">
          <div>
            <h3 className="m-0 text-[13px] font-semibold text-k-text">提供商列表</h3>
            <p className="mt-1 mb-0 text-[10px] text-k-text-subtle">共 {data.total.toLocaleString()} 个提供商</p>
          </div>
        </div>
        <Table<AIProvider>
          columns={providerColumns}
          dataSource={data.items}
          loading={loading}
          locale={{ emptyText: <Empty description="暂无 AI 提供商" image={Empty.PRESENTED_IMAGE_SIMPLE} /> }}
          onChange={(pagination) => {
            const pageSize = pagination.pageSize ?? query.pageSize
            setQuery((current) => ({
              ...current,
              page: pageSize === current.pageSize ? (pagination.current ?? 1) : 1,
              pageSize,
            }))
          }}
          pagination={{
            current: query.page,
            pageSize: query.pageSize,
            pageSizeOptions: [10, 20, 50, 100],
            showSizeChanger: true,
            showTotal: (total) => `共 ${total} 个`,
            total: data.total,
          }}
          rowKey="id"
          scroll={{ x: 1050 }}
        />
      </Card>

      <Modal
        confirmLoading={providerSaving}
        destroyOnHidden
        okText={editingProvider ? '保存' : '创建'}
        onCancel={() => setProviderModalOpen(false)}
        onOk={saveProvider}
        open={providerModalOpen}
        title={editingProvider ? '编辑 AI 提供商' : '新增 AI 提供商'}
      >
        <Form className="pt-3" form={providerForm} layout="vertical" requiredMark={false}>
          <Form.Item label="提供商名称" name="provider_name" rules={[{ required: true, message: '请输入提供商名称' }]}>
            <Input maxLength={100} placeholder="例如 OpenAI" prefix={<ApiOutlined />} />
          </Form.Item>
          <Form.Item label="API 协议" name="api_protocol" rules={[{ required: true, message: '请选择 API 协议' }]}>
            <Select options={protocolOptions} />
          </Form.Item>
          <Form.Item label="Base URL" name="base_url">
            <Input placeholder="留空则使用协议默认地址" />
          </Form.Item>
          <Form.Item label="API Key" name="api_key">
            <Input.Password autoComplete="new-password" placeholder="请输入 API Key" />
          </Form.Item>
        </Form>
      </Modal>

      <Drawer
        destroyOnHidden
        extra={<Button icon={<PlusOutlined />} onClick={showCreateModel} type="primary">新增模型</Button>}
        onClose={() => setSelectedProviderID(undefined)}
        open={Boolean(selectedProvider)}
        size="large"
        title={<span className="inline-flex items-center gap-2"><RobotOutlined />{selectedProvider?.provider_name} · 模型管理</span>}
      >
        <Alert
          className="mb-4"
          message={`${selectedProvider?.models.length ?? 0} 个可用模型`}
          description="设为默认模型时，当前提供商原有的默认模型会自动取消。"
          showIcon
          type="info"
        />
        <Table<AIModel>
          columns={modelColumns}
          dataSource={selectedProvider?.models ?? []}
          locale={{ emptyText: <Empty description="暂无模型，请先新增" image={Empty.PRESENTED_IMAGE_SIMPLE} /> }}
          pagination={false}
          rowKey="id"
          scroll={{ x: 890 }}
          size="small"
        />
      </Drawer>

      <Modal
        confirmLoading={modelSaving}
        destroyOnHidden
        okText={editingModel ? '保存' : '创建'}
        onCancel={() => setModelModalOpen(false)}
        onOk={saveModel}
        open={modelModalOpen}
        title={editingModel ? '编辑模型' : '新增模型'}
        width={720}
      >
        <Form className="pt-3" form={modelForm} layout="vertical" requiredMark={false}>
          <div className="grid grid-cols-2 gap-x-4 max-[620px]:grid-cols-1">
            <Form.Item label="所属提供商" name="provider_id" rules={[{ required: true, message: '请选择提供商' }]}>
              <Select disabled={!editingModel} options={providerLabels} />
            </Form.Item>
            <Form.Item label="模型显示名称" name="model_name" rules={[{ required: true, message: '请输入模型名称' }]}>
              <Input placeholder="例如 GPT-5" />
            </Form.Item>
            <Form.Item label="API 模型标识" name="model_id" rules={[{ required: true, message: '请输入模型标识' }]}>
              <Input className="font-mono" placeholder="例如 gpt-5" />
            </Form.Item>
            <Form.Item label="默认模型" name="is_default" rules={[{ required: true }]}>
              <Select options={[{ label: '是', value: 1 }, { label: '否', value: 2 }]} />
            </Form.Item>
            <Form.Item label="推理模式" name="reasoning_enabled" rules={[{ required: true }]}>
              <Select options={statusOptions} />
            </Form.Item>
            <Form.Item label="推理强度" name="reasoning_effort" rules={[{ required: true }]}>
              <Select options={[{ label: 'Low', value: 'low' }, { label: 'Medium', value: 'medium' }, { label: 'High', value: 'high' }]} />
            </Form.Item>
            <Form.Item label="上下文窗口（Token）" name="token_context_window" rules={[{ required: true }]}>
              <InputNumber className="w-full" min={0} />
            </Form.Item>
            <Form.Item label="最大输出（Token）" name="token_max_output_tokens" rules={[{ required: true }]}>
              <InputNumber className="w-full" min={0} />
            </Form.Item>
            <Form.Item label="工具调用" name="capability_tool_use" rules={[{ required: true }]}>
              <Select options={statusOptions} />
            </Form.Item>
            <Form.Item label="视觉能力" name="capability_vision" rules={[{ required: true }]}>
              <Select options={statusOptions} />
            </Form.Item>
            <Form.Item label="结构化输出" name="capability_structured_output" rules={[{ required: true }]}>
              <Select options={statusOptions} />
            </Form.Item>
          </div>
        </Form>
      </Modal>
    </div>
  )
}
