import { useEffect, useState } from 'react'
import { App, Alert, Button, Card, Empty, Input, Space, Table, Tag, Typography } from 'antd'
import { ReloadOutlined, SearchOutlined, SyncOutlined } from '@ant-design/icons'
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
      void message.success(`已同步 ${result.count.toLocaleString()} 个模型`)
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
        <div className="font-medium text-k-text">{item.name}</div>
        <Typography.Text className="block! font-mono text-xs!" copyable={{ text: item.id }} type="secondary">{item.id}</Typography.Text>
        {item.description && <Typography.Text className="mt-1 block! max-w-[290px] text-xs!" ellipsis={{ tooltip: item.description }} type="secondary">{item.description}</Typography.Text>}
      </div>,
    },
    {
      title: '实验室', dataIndex: 'lab', key: 'lab', width: 130,
      render: (lab: string, item: ModelCatalogItem) => <div><div>{lab || '—'}</div>{item.family && <Typography.Text type="secondary" className="text-xs!">{item.family}</Typography.Text>}</div>,
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
        <Input.Search aria-label="检索模型目录" allowClear className="max-w-[520px]" enterButton={<SearchOutlined />} placeholder="搜索模型名称、标识、实验室、系列或描述" value={draftKeyword} onChange={event => setDraftKeyword(event.target.value)} onSearch={search} />
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
        scroll={{ x: 1180 }}
        size="middle"
      />
    </Card>
  </div>
}
