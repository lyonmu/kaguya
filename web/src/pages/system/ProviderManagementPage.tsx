import { useEffect, useMemo, useState } from 'react'
import {
  ApiOutlined,
  DeleteOutlined,
  EditOutlined,
  ExperimentOutlined,
  EyeOutlined,
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
  fetchProviderAPIKey,
  fetchModelCatalog,
  fetchProviderCatalogPage,
  fetchProviderLabels,
  testModel,
  updateModel,
  updateProvider,
} from '../../features/providers/api'
import type {
  AIModel,
  AIProvider,
  LabelOption,
  ModelCatalogItem,
  ModelPayload,
  ProviderCatalogItem,
  ProviderPayload,
  ProviderProtocol,
} from '../../features/providers/types'
import {
  DEFAULT_PROVIDER_QUERY,
  useProviders,
} from '../../features/providers/useProviders'

const protocolOptions = [
  { label: 'Chat · /chat/completions', value: 'openai-chat' },
  { label: 'Response · /responses', value: 'openai-response' },
  { label: 'Message · /messages', value: 'anthropic' },
]

const statusOptions = [
  { label: '启用', value: 1 },
  { label: '禁用', value: 2 },
]

const protocolLabel: Record<ProviderProtocol, string> = {
  'openai-chat': 'Chat',
  anthropic: 'Message',
  'openai-response': 'Response',
}

// 与后端 consts.ProviderProtocol.DefaultRequestPath 保持一致。
const protocolDefaultPath: Record<ProviderProtocol, string> = {
  'openai-chat': '/chat/completions',
  'openai-response': '/responses',
  anthropic: '/messages',
}

// joinRequestURL 与后端拼接规则一致：只在两侧去掉多余斜杠。
const joinRequestURL = (baseURL: string, requestPath: string) =>
  `${baseURL.replace(/\/+$/, '')}/${requestPath.replace(/^\/+/, '')}`

// modelTestKey 只包含决定真实请求的字段：测试通过后其中任一字段改动，
// 之前的结论都不再成立，需要重新测试。
const modelTestKey = (values: Partial<ModelPayload>) =>
  JSON.stringify([
    values.provider_id ?? '',
    values.model_id ?? '',
    values.api_protocol ?? '',
    values.request_path ?? '',
  ])

interface FilterValues {
  providerName?: string
}

export function ProviderManagementPage() {
  const { message } = App.useApp()
  const [filterForm] = Form.useForm<FilterValues>()
  const [providerForm] = Form.useForm<ProviderPayload & { catalog_provider_id?: string }>()
  const [modelForm] = Form.useForm<ModelPayload & { catalog_model_id?: string }>()
  const { data, error, loading, query, reload, setQuery } = useProviders()
  const [providerModalOpen, setProviderModalOpen] = useState(false)
  const [editingProvider, setEditingProvider] = useState<AIProvider>()
  const [providerSaving, setProviderSaving] = useState(false)
  const [modelModalOpen, setModelModalOpen] = useState(false)
  const [editingModel, setEditingModel] = useState<AIModel>()
  const [modelSaving, setModelSaving] = useState(false)
  const [modelTesting, setModelTesting] = useState(false)
  // 已通过测试的配置指纹：为空表示当前配置没有测试结论，不能保存。
  const [testedModelKey, setTestedModelKey] = useState<string>()
  const [selectedProviderID, setSelectedProviderID] = useState<string>()
  const [providerLabels, setProviderLabels] = useState<LabelOption[]>([])
  const [revealingID, setRevealingID] = useState<string>()
  const [revealedKey, setRevealedKey] = useState<{ name: string; value: string }>()
  const [modelCatalog, setModelCatalog] = useState<ModelCatalogItem[]>([])
  const [catalogLoading, setCatalogLoading] = useState(false)
  const [catalogKeyword, setCatalogKeyword] = useState('')
  const [providerCatalog, setProviderCatalog] = useState<ProviderCatalogItem[]>([])
  const [providerCatalogLoading, setProviderCatalogLoading] = useState(false)
  const [providerCatalogKeyword, setProviderCatalogKeyword] = useState('')
  const watchedProtocol = Form.useWatch<ProviderProtocol>('api_protocol', modelForm)
  const watchedPath = Form.useWatch<string>('request_path', modelForm)
  const watchedModel = Form.useWatch<Partial<ModelPayload> | undefined>([], modelForm)
  const modelTested = testedModelKey !== undefined && testedModelKey === modelTestKey(watchedModel ?? {})

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

  useEffect(() => {
    if (!selectedProviderID) return
    const controller = new AbortController()
    // 目录是全量同步的模型列表，输入关键词后由服务端检索，不在前端全量拉取。
    const timer = window.setTimeout(() => {
      setCatalogLoading(true)
      fetchModelCatalog(catalogKeyword, controller.signal)
        .then(result => setModelCatalog(result.items ?? []))
        .catch(() => setModelCatalog([]))
        .finally(() => { if (!controller.signal.aborted) setCatalogLoading(false) })
    }, 250)
    return () => { window.clearTimeout(timer); controller.abort() }
  }, [selectedProviderID, catalogKeyword])

  // 新增提供商时按目录搜索预填名称与 BaseURL，编辑已有提供商不重新拉取。
  useEffect(() => {
    if (!providerModalOpen || editingProvider) return
    const controller = new AbortController()
    const timer = window.setTimeout(() => {
      setProviderCatalogLoading(true)
      fetchProviderCatalogPage(providerCatalogKeyword, 1, 50, controller.signal)
        .then(result => setProviderCatalog(result.items ?? []))
        .catch(() => setProviderCatalog([]))
        .finally(() => { if (!controller.signal.aborted) setProviderCatalogLoading(false) })
    }, 250)
    return () => { window.clearTimeout(timer); controller.abort() }
  }, [providerModalOpen, editingProvider, providerCatalogKeyword])

  const showCreateProvider = () => {
    setEditingProvider(undefined)
    // 重新打开时清空上一次的目录检索词，避免选项列表与空输入框不一致。
    setProviderCatalogKeyword('')
    providerForm.setFieldsValue({
      catalog_provider_id: undefined,
      provider_name: '',
      provider_type: 'normal',
      api_key: '',
      base_url: '',
    })
    setProviderModalOpen(true)
  }

  const showEditProvider = (provider: AIProvider) => {
    setEditingProvider(provider)
    providerForm.setFieldsValue({
      catalog_provider_id: undefined,
      provider_name: provider.provider_name,
      provider_type: provider.provider_type,
      // 后端只返回掩码，因此不预填；留空表示保留已存储的密钥。
      api_key: '',
      base_url: provider.base_url,
    })
    setProviderModalOpen(true)
  }

  const selectCatalogProvider = (id: string) => {
    const item = providerCatalog.find(provider => provider.id === id)
    if (!item) return
    providerForm.setFieldsValue({ provider_name: item.name, base_url: item.api })
  }

  const saveProvider = async () => {
    try {
      const { catalog_provider_id: _, ...values } = await providerForm.validateFields()
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

  // 明文只在用户点击查看时按需拉取，不随列表响应下发，也不长期保存在页面状态里。
  const revealAPIKey = async (provider: AIProvider) => {
    setRevealingID(provider.id)
    try {
      const result = await fetchProviderAPIKey(provider.id)
      setRevealedKey({ name: provider.provider_name, value: result.api_key || '未配置' })
    } catch (requestError) {
      message.error(requestError instanceof Error ? requestError.message : '读取 API Key 失败')
    } finally {
      setRevealingID(undefined)
    }
  }

  const showCreateModel = () => {
    if (!selectedProvider) return
    setEditingModel(undefined)
    // 每次打开都重新测试：上一次的结论不能用于新配置。
    setTestedModelKey(undefined)
    // 重新打开时清空上一次的目录检索词，避免选项列表与空输入框不一致。
    setCatalogKeyword('')
    modelForm.setFieldsValue({
      provider_id: selectedProvider.id,
      catalog_model_id: undefined,
      model_name: "",
      model_id: "",
      api_protocol: 'openai-chat',
      request_path: protocolDefaultPath['openai-chat'],
      reasoning_enabled: 1,
      reasoning_effort: "medium",
      token_context_window: 0,
      token_max_output_tokens: 0,
      capability_tool_use: 1,
      capability_vision: 1,
      capability_structured_output: 1,
    });
    setModelModalOpen(true)
  }

  const showEditModel = (model: AIModel) => {
    setEditingModel(model)
    setTestedModelKey(undefined)
    modelForm.setFieldsValue({
      provider_id: model.provider_id,
      model_name: model.model_name,
      model_id: model.model_id,
      api_protocol: model.api_protocol,
      request_path: model.request_path,
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

  // 测试与保存使用同一份表单值：后端用待保存的配置真实调用一次提供商模型。
  const checkModel = async () => {
    try {
      const { catalog_model_id: _, ...values } = await modelForm.validateFields()
      setModelTesting(true)
      const result = await testModel(values)
      setTestedModelKey(modelTestKey(values))
      const reply = (result?.reply ?? '').replace(/\s+/g, ' ').trim()
      message.success(`模型测试通过（${result?.duration_ms ?? 0} ms）${reply ? `：${reply.slice(0, 40)}` : ''}`)
    } catch (testError) {
      // 失败或改动配置后不保留结论，保存按钮保持不可用。
      setTestedModelKey(undefined)
      if (testError instanceof Error) message.error(testError.message)
    } finally {
      setModelTesting(false)
    }
  }

  const saveModel = async () => {
    try {
      const { catalog_model_id: _, ...values } = await modelForm.validateFields()
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

  const selectCatalogModel = (id: string) => {
    const item = modelCatalog.find(model => model.id === id)
    if (!item) return
    modelForm.setFieldsValue({
      model_name: item.name,
      model_id: item.model_id,
      reasoning_enabled: item.reasoning_enabled,
      reasoning_effort: 'medium',
      token_context_window: item.token_context_window,
      token_max_output_tokens: item.token_max_output_tokens,
      capability_tool_use: item.capability_tool_use,
      capability_vision: item.capability_vision,
      capability_structured_output: item.capability_structured_output,
    })
  }

  // 切换协议时只改写默认路径，不覆盖用户已经自定义的路径。
  const selectProtocol = (protocol: ProviderProtocol) => {
    const current = modelForm.getFieldValue('request_path') as string | undefined
    const defaults = Object.values(protocolDefaultPath)
    if (!current || defaults.includes(current)) {
      modelForm.setFieldsValue({ request_path: protocolDefaultPath[protocol] })
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
      title: '类型', dataIndex: 'provider_type', key: 'provider_type', width: 130,
      render: (value: string) => <Tag>{value === 'opencode-go' ? 'OpenCode Go' : '标准'}</Tag>,
    },
    {
      title: '模型协议',
      key: 'model_protocols',
      width: 180,
      render: (_, provider) => {
        const protocols = Array.from(new Set((provider.models ?? []).map(model => model.api_protocol)))
        if (!protocols.length) return <span className="text-[11px] text-k-text-subtle">—</span>
        return (
          <Space size={[0, 4]} wrap>
            {protocols.map(protocol => (
              <Tag color={protocol === 'anthropic' ? 'orange' : 'blue'} key={protocol}>
                {protocolLabel[protocol] ?? protocol}
              </Tag>
            ))}
          </Space>
        )
      },
    },
    {
      title: 'Base URL',
      dataIndex: 'base_url',
      key: 'base_url',
      ellipsis: true,
      render: (value: string) => (
        <Tooltip title={value || '未配置 Base URL'}>
          <span className="font-mono text-[11px] text-k-text-muted">
            {value || '未配置'}
          </span>
        </Tooltip>
      ),
    },
    {
      title: 'API Key',
      dataIndex: 'api_key',
      key: 'api_key',
      width: 190,
      render: (value: string, record: AIProvider) =>
        record.api_key_set ? (
          <Space size={2}>
            {/* 后端只返回掩码，明文需要显式查看。 */}
            <span className="font-mono text-[11px] text-k-text-muted">{value}</span>
            <Tooltip title="查看完整 API Key">
              <Button
                aria-label={`查看 ${record.provider_name} 的 API Key`}
                icon={<EyeOutlined />}
                loading={revealingID === record.id}
                onClick={() => revealAPIKey(record)}
                size="small"
                type="text"
              />
            </Tooltip>
          </Space>
        ) : (
          <span className="text-[11px] text-k-text-subtle">未配置</span>
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
      render: (value: string) => (
        <span className="font-medium text-k-text">
          {value}
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
      title: '协议',
      dataIndex: 'api_protocol',
      key: 'api_protocol',
      width: 100,
      render: (value: ProviderProtocol) => (
        <Tag color={value === 'anthropic' ? 'orange' : 'blue'}>{protocolLabel[value] ?? value}</Tag>
      ),
    },
    {
      title: '请求路径',
      dataIndex: 'request_path',
      key: 'request_path',
      width: 200,
      ellipsis: true,
      render: (value: string) => <Tooltip title={value}><span className="font-mono text-[11px] text-k-text-muted">{value}</span></Tooltip>,
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
    <div className="mx-auto w-full max-w-[1480px] px-6 pt-5 pb-8 max-[900px]:px-5 max-[620px]:px-3.5 max-[620px]:pt-5">
      <div className="mb-4 flex items-end justify-between gap-6 max-[620px]:items-start">
        <div>
          <span className="mb-1.5 block text-[9px] font-bold tracking-[1.4px] text-k-text-subtle">SYSTEM / AI CONFIGURATION</span>
          <h2 className="m-0 text-[20px] font-semibold tracking-[-0.25px] text-k-text">AI 提供商管理</h2>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} loading={loading} onClick={reload}>刷新</Button>
          <Button icon={<PlusOutlined />} onClick={showCreateProvider} type="primary">新增提供商</Button>
        </Space>
      </div>

      <Card className="mb-3 border-k-border! bg-k-surface! shadow-sm shadow-black/5">
        <Form
          form={filterForm}
          layout="vertical"
          onFinish={(values) => setQuery({
            page: 1,
            pageSize: query.pageSize,
            providerName: values.providerName?.trim() || undefined,
          })}
          requiredMark={false}
        >
          <div className="grid grid-cols-[minmax(220px,1fr)_auto] items-end gap-3.5 max-[800px]:grid-cols-1">
            <Form.Item className="mb-0!" label="提供商名称" name="providerName">
              <Input allowClear placeholder="搜索提供商" prefix={<SearchOutlined className="text-k-text-subtle" />} />
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

      {error ? <Alert className="mb-3" title="提供商加载失败" description={error} showIcon type="error" /> : null}

      <Card className="overflow-hidden border-k-border! bg-k-surface! shadow-sm shadow-black/5" styles={{ body: { padding: 0 } }}>
        <div className="flex min-h-14 items-center justify-between border-b border-k-border-soft px-4 py-2.5">
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
          {!editingProvider ? <Form.Item label="从已同步目录选择" name="catalog_provider_id" tooltip="来自 models.dev 提供商目录；选择后自动填充名称与 Base URL，仍可手动修改。">
            <Select
              allowClear
              filterOption={false}
              loading={providerCatalogLoading}
              onChange={selectCatalogProvider}
              onSearch={setProviderCatalogKeyword}
              options={providerCatalog.map(item => ({ label: `${item.name} · ${item.api}`, value: item.id }))}
              placeholder={providerCatalog.length ? '搜索提供商名称、标识或 API 地址' : '请先在系统配置中同步目录'}
              showSearch
            />
          </Form.Item> : null}
          <Form.Item label="提供商名称" name="provider_name" rules={[{ required: true, message: '请输入提供商名称' }]}>
            <Input maxLength={100} placeholder="例如 OpenAI" prefix={<ApiOutlined />} />
          </Form.Item>
          <Form.Item label="提供商类型" name="provider_type" rules={[{ required: true }]} tooltip="OpenCode Go 会在请求头 x-opencode-session 中传入会话 ID。">
            <Select options={[{ label: '标准（normal）', value: 'normal' }, { label: 'OpenCode Go', value: 'opencode-go' }]} />
          </Form.Item>
          <Form.Item label="Base URL" name="base_url" rules={[{ required: true, message: '请输入 Base URL' }, { type: 'url', message: '请输入有效的 URL' }, { pattern: /^https?:\/\//, message: '仅支持 HTTP(S) URL' }]} tooltip="提供商地址，请求时与模型的请求路径拼接；例如 https://api.deepseek.com。">
            <Input placeholder="例如 https://api.deepseek.com" />
          </Form.Item>
          <Form.Item label="API Key" name="api_key" tooltip={editingProvider ? '留空表示保留已存储的密钥；填写则覆盖。' : undefined}>
            <Input.Password autoComplete="new-password" placeholder={editingProvider ? '留空则不修改已存储的密钥' : '请输入 API Key'} />
          </Form.Item>
        </Form>
      </Modal>

      {/* 明文只在这个弹窗里展示，关闭即从页面状态移除。 */}
      <Modal
        footer={<Button onClick={() => setRevealedKey(undefined)} type="primary">关闭</Button>}
        onCancel={() => setRevealedKey(undefined)}
        open={Boolean(revealedKey)}
        title={revealedKey ? `${revealedKey.name} 的 API Key` : 'API Key'}
      >
        <Alert
          className="mb-3"
          description="明文只在本次查看时下发，请勿截图或转发。"
          showIcon
          type="warning"
        />
        <Typography.Paragraph className="mb-0!" copyable={{ text: revealedKey?.value }}>
          <span className="font-mono text-[12px] break-all">{revealedKey?.value}</span>
        </Typography.Paragraph>
      </Modal>

      <Drawer
        destroyOnHidden
        extra={<Tooltip title="此处管理模型信息；默认对话模型和后台任务模型请在系统配置中选择。"><Button icon={<PlusOutlined />} onClick={showCreateModel} type="primary">新增模型</Button></Tooltip>}
        onClose={() => setSelectedProviderID(undefined)}
        open={Boolean(selectedProvider)}
        size="large"
        title={<span className="inline-flex items-center gap-2"><RobotOutlined />{selectedProvider?.provider_name} · 模型管理</span>}
      >
        <Table<AIModel>
          columns={modelColumns}
          dataSource={selectedProvider?.models ?? []}
          locale={{ emptyText: <Empty description="暂无模型，请先新增" image={Empty.PRESENTED_IMAGE_SIMPLE} /> }}
          pagination={false}
          rowKey="id"
          scroll={{ x: 1090 }}
          size="small"
        />
      </Drawer>

      <Modal
        destroyOnHidden
        footer={
          <Space>
            <Button onClick={() => setModelModalOpen(false)}>取消</Button>
            <Tooltip title="向该模型发送 Hi! 验证提供商配置；测试通过后才能保存。">
              <Button icon={<ExperimentOutlined />} loading={modelTesting} onClick={checkModel}>
                测试
              </Button>
            </Tooltip>
            {/* 禁用态按钮不触发鼠标事件，用 span 包裹才能显示提示。 */}
            <Tooltip title={modelTested ? undefined : '请先测试通过'}>
              <span>
                <Button disabled={!modelTested} loading={modelSaving} onClick={saveModel} type="primary">
                  {editingModel ? '保存' : '创建'}
                </Button>
              </span>
            </Tooltip>
          </Space>
        }
        onCancel={() => setModelModalOpen(false)}
        open={modelModalOpen}
        title={editingModel ? '编辑模型' : '新增模型'}
        width={720}
      >
        <Form className="pt-3" form={modelForm} layout="vertical" requiredMark={false}>
          <div className="grid grid-cols-2 gap-x-4 max-[620px]:grid-cols-1">
            {!editingModel ? <Form.Item className="col-span-2 max-[620px]:col-span-1" label="从已同步目录选择" name="catalog_model_id" tooltip="来自 models.dev；选择后自动填充下方信息。目录不保证当前提供商一定开放该模型。">
              <Select
                allowClear
                filterOption={false}
                loading={catalogLoading}
                onChange={selectCatalogModel}
                onSearch={setCatalogKeyword}
                options={modelCatalog.map(model => ({ label: `${model.name} · ${model.provider_name}/${model.model_id}`, value: model.id }))}
                placeholder={modelCatalog.length ? '搜索模型名称或标识' : '请先在系统配置中同步目录'}
                showSearch
              />
            </Form.Item> : null}
            <Form.Item label="所属提供商" name="provider_id" rules={[{ required: true, message: '请选择提供商' }]}>
              <Select disabled={!editingModel} options={providerLabels} />
            </Form.Item>
            <Form.Item label="模型显示名称" name="model_name" rules={[{ required: true, message: '请输入模型名称' }]}>
              <Input placeholder="例如 GPT-5" />
            </Form.Item>
            <Form.Item label="API 模型标识" name="model_id" rules={[{ required: true, message: '请输入模型标识' }]}>
              <Input className="font-mono" placeholder="例如 gpt-5" />
            </Form.Item>
            <Form.Item
              label="请求协议"
              name="api_protocol"
              rules={[{ required: true, message: '请选择请求协议' }]}
              tooltip="决定运行时使用哪套请求实现。"
            >
              <Select onChange={selectProtocol} options={protocolOptions} />
            </Form.Item>
            <Form.Item
              className="col-span-2 max-[620px]:col-span-1"
              label="请求路径"
              name="request_path"
              rules={[{ required: true, message: '请输入请求路径' }, { pattern: /^\//, message: '请求路径需以 / 开头' }]}
              tooltip="与提供商 Base URL 拼接成最终请求地址；切换协议会填入默认路径，可直接修改。"
              extra={selectedProvider?.base_url && watchedPath ? (
                <span className="text-[11px]">最终请求：<span className="font-mono">{joinRequestURL(selectedProvider.base_url, watchedPath)}</span></span>
              ) : undefined}
            >
              <Input className="font-mono" placeholder={protocolDefaultPath[watchedProtocol ?? 'openai-chat']} />
            </Form.Item>
            <Form.Item label="推理模式" name="reasoning_enabled" rules={[{ required: true }]}>
              <Select options={statusOptions} />
            </Form.Item>
            <Form.Item label="推理强度" name="reasoning_effort" rules={[{ required: true }]}>
              <Select options={[
                { label: 'Minimal', value: 'minimal' }, { label: 'Low', value: 'low' }, { label: 'Medium', value: 'medium' },
                { label: 'High', value: 'high' }, { label: 'XHigh', value: 'xhigh' }, { label: 'Max', value: 'max' },
              ]} />
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
