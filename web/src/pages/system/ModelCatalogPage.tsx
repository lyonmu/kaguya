import { useEffect, useState } from 'react'
import { App, Alert, Button, Card, Descriptions, Drawer, Empty, Input, Space, Table, Tag, Typography } from 'antd'
import { EyeOutlined, ReloadOutlined, SearchOutlined, SyncOutlined } from '@ant-design/icons'
import { fetchModelCatalogPage, syncModelCatalog } from '../../features/providers/api'
import type { ModelCatalogItem, ModelCatalogResponse } from '../../features/providers/types'
import { fetchSystemInfo } from '../../features/system-info/api'
import type { SystemInfo } from '../../features/system-info/types'

const PAGE_SIZE = 20

const formatNumber = (value: number) => value > 0 ? value.toLocaleString() : '—'
const formatTime = (value?: string) => value ? new Date(value).toLocaleString() : '尚未同步'

export function ModelCatalogPage() {
  const { message } = App.useApp()
  const [info, setInfo] = useState<SystemInfo>()
  const [catalog, setCatalog] = useState<ModelCatalogResponse>({ total: 0, items: [], page: 1, page_size: PAGE_SIZE })
  const [selectedModel, setSelectedModel] = useState<ModelCatalogItem>()
  const [draftKeyword, setDraftKeyword] = useState('')
  const [keyword, setKeyword] = useState('')
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [syncing, setSyncing] = useState(false)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError('')
    Promise.all([
      fetchSystemInfo(controller.signal),
      fetchModelCatalogPage(keyword, page, PAGE_SIZE, controller.signal),
    ]).then(([systemInfo, result]) => {
      if (controller.signal.aborted) return
      setInfo(systemInfo)
      setCatalog(result)
    }).catch(error => {
      if (!controller.signal.aborted) setError(error instanceof Error ? error.message : '模型目录加载失败')
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [keyword, page, revision])

  const search = () => {
    setPage(1)
    setKeyword(draftKeyword.trim())
  }
  const syncNow = async () => {
    setSyncing(true)
    try {
      const result = await syncModelCatalog()
      void message.success(`已同步 ${result.provider_count.toLocaleString()} 个提供商、${result.count.toLocaleString()} 个模型`)
      setPage(1)
      setRevision(value => value + 1)
    } catch (error) {
      void message.error(error instanceof Error ? error.message : '同步失败')
    } finally {
      setSyncing(false)
    }
  }

  const columns = [
    {
      title: '模型', key: 'model', width: 310,
      render: (_: unknown, item: ModelCatalogItem) => <div className="min-w-0">
        <Button className="h-auto! p-0! text-left! font-medium!" type="link" onClick={() => setSelectedModel(item)}>{item.name}</Button>
        <Typography.Text className="block! font-mono text-xs!" copyable={{ text: item.id }} type="secondary">{item.id}</Typography.Text>
        {item.description && <Typography.Text className="mt-1 block! max-w-[290px] text-xs!" ellipsis={{ tooltip: item.description }} type="secondary">{item.description}</Typography.Text>}
      </div>,
    },
    {
      title: '提供商', dataIndex: 'provider_name', key: 'provider_name', width: 130,
      render: (providerName: string, item: ModelCatalogItem) => <div><div>{providerName || item.lab || '—'}</div>{item.family && <Typography.Text type="secondary" className="text-xs!">{item.family}</Typography.Text>}</div>,
    },
    { title: '上下文', dataIndex: 'token_context_window', key: 'context', width: 105, align: 'right' as const, render: formatNumber },
    { title: '最大输出', dataIndex: 'token_max_output_tokens', key: 'output', width: 105, align: 'right' as const, render: formatNumber },
    {
      title: '输入', dataIndex: 'input_modalities', key: 'input', width: 120,
      render: (values: string[]) => values?.length ? <Space size={[0, 4]} wrap>{values.map(value => <Tag key={value}>{value}</Tag>)}</Space> : '—',
    },
    {
      title: '能力', key: 'capabilities', width: 175,
      render: (_: unknown, item: ModelCatalogItem) => {
        const values = [item.reasoning_enabled === 1 && '推理', item.capability_tool_use === 1 && 'Tool', item.capability_vision === 1 && '视觉', item.capability_structured_output === 1 && 'JSON'].filter(Boolean) as string[]
        return values.length ? <Space size={[0, 4]} wrap>{values.map(value => <Tag color="blue" key={value}>{value}</Tag>)}</Space> : '—'
      },
    },
    { title: '发布日期', dataIndex: 'release_date', key: 'release', width: 112, render: (value: string) => value || '—' },
    { title: '更新时间', dataIndex: 'last_updated', key: 'updated', width: 112, render: (value: string) => value || '—' },
    { title: '操作', key: 'actions', fixed: 'right' as const, width: 76, render: (_: unknown, item: ModelCatalogItem) => <Button aria-label={`查看 ${item.name} 详情`} icon={<EyeOutlined />} type="text" onClick={() => setSelectedModel(item)}>查看</Button> },
  ]

  return <div className="mx-auto w-full max-w-[1480px] px-6 py-5 max-[620px]:px-3.5">
    <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div><h2 className="m-0 text-[20px] font-semibold text-k-text">模型目录</h2><p className="mt-1 mb-0 text-sm text-k-text-muted">检索已同步到本地的模型信息，默认按发布日期从新到旧排列</p></div>
      <Space wrap>
        <Button icon={<ReloadOutlined />} loading={loading} onClick={() => setRevision(value => value + 1)}>重新加载</Button>
        <Button icon={<SyncOutlined />} loading={syncing} onClick={syncNow}>立即同步</Button>
      </Space>
    </div>
    {error && <Alert type="error" title={error} showIcon className="mb-3" action={<Button onClick={() => setRevision(value => value + 1)}>重试</Button>} />}
    <Card styles={{ body: { padding: 16 } }}>
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <Input.Search aria-label="检索模型目录" allowClear className="max-w-[520px]" enterButton={<SearchOutlined />} placeholder="搜索模型名称、标识、提供商、系列或描述" value={draftKeyword} onChange={event => setDraftKeyword(event.target.value)} onSearch={search} />
        <div className="min-w-0 text-right text-xs text-k-text-muted">
          <div className="max-w-[620px] truncate" title={info?.model_sync_url}>来源：{info?.model_sync_url ?? '—'}</div>
          <div>共 {catalog.total.toLocaleString()} 项 · 最近同步：{formatTime(info?.model_sync_last_success_at)}</div>
        </div>
      </div>
      <Table<ModelCatalogItem>
        columns={columns}
        dataSource={catalog.items}
        loading={loading}
        locale={{ emptyText: <Empty description={keyword ? '没有匹配的模型' : '暂无模型，请先立即同步或到系统配置启用定时同步'} /> }}
        pagination={{ current: page, pageSize: PAGE_SIZE, total: catalog.total, showSizeChanger: false, showTotal: total => `共 ${total.toLocaleString()} 项`, onChange: setPage }}
        rowKey="id"
        scroll={{ x: 1260 }}
        size="middle"
      />
    </Card>
    <Drawer
      destroyOnHidden
      open={!!selectedModel}
      placement="right"
      title="模型详情"
      size={640}
      onClose={() => setSelectedModel(undefined)}
    >
      {selectedModel && <div>
        <div className="mb-5 border-b border-k-border-soft pb-4">
          <h3 className="m-0 text-xl font-semibold text-k-text">{selectedModel.name}</h3>
          <Typography.Text className="mt-1 block! font-mono text-xs!" copyable={{ text: selectedModel.id }} type="secondary">{selectedModel.id}</Typography.Text>
          <Typography.Paragraph className="mt-3 mb-0! text-sm!" type="secondary">{selectedModel.description || '暂无模型描述'}</Typography.Paragraph>
        </div>
        <Descriptions bordered column={{ xs: 1, sm: 2 }} size="small" title="基本信息">
          <Descriptions.Item label="提供商">{selectedModel.provider_name || selectedModel.lab || '—'}</Descriptions.Item>
          <Descriptions.Item label="模型系列">{selectedModel.family || '—'}</Descriptions.Item>
          <Descriptions.Item label="发布日期">{selectedModel.release_date || '—'}</Descriptions.Item>
          <Descriptions.Item label="更新时间">{selectedModel.last_updated || '—'}</Descriptions.Item>
        </Descriptions>
        <Descriptions bordered className="mt-5" column={{ xs: 1, sm: 2 }} size="small" title="Token 限额">
          <Descriptions.Item label="上下文窗口">{formatNumber(selectedModel.token_context_window)}</Descriptions.Item>
          <Descriptions.Item label="最大输出">{formatNumber(selectedModel.token_max_output_tokens)}</Descriptions.Item>
        </Descriptions>
        <Descriptions bordered className="mt-5" column={1} size="small" title="模态与能力">
          <Descriptions.Item label="输入模态">{selectedModel.input_modalities?.length ? <Space size={[0, 4]} wrap>{selectedModel.input_modalities.map(value => <Tag key={value}>{value}</Tag>)}</Space> : '—'}</Descriptions.Item>
          <Descriptions.Item label="推理能力"><CapabilityStatus enabled={selectedModel.reasoning_enabled === 1} /></Descriptions.Item>
          <Descriptions.Item label="工具调用"><CapabilityStatus enabled={selectedModel.capability_tool_use === 1} /></Descriptions.Item>
          <Descriptions.Item label="视觉输入"><CapabilityStatus enabled={selectedModel.capability_vision === 1} /></Descriptions.Item>
          <Descriptions.Item label="结构化输出"><CapabilityStatus enabled={selectedModel.capability_structured_output === 1} /></Descriptions.Item>
        </Descriptions>
        <div className="mt-5 rounded-lg border border-k-border-soft bg-k-canvas p-3 text-xs text-k-text-muted">
          数据来自本地同步缓存：<Typography.Text className="text-xs!" copyable={{ text: info?.model_sync_url ?? '' }}>{info?.model_sync_url ?? '—'}</Typography.Text>
        </div>
      </div>}
    </Drawer>
  </div>
}

function CapabilityStatus({ enabled }: { enabled: boolean }) {
  return enabled ? <Tag color="green">支持</Tag> : <Tag>不支持</Tag>
}
