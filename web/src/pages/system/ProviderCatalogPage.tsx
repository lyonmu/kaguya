import { useEffect, useState } from 'react'
import {
  LinkOutlined,
  ReloadOutlined,
  SearchOutlined,
  SyncOutlined,
} from '@ant-design/icons'
import {
  Alert,
  App,
  Button,
  Empty,
  Input,
  Pagination,
  Space,
  Spin,
  Tooltip,
  Typography,
} from 'antd'
import {
  fetchProviderCatalogPage,
  syncModelCatalog,
} from '../../features/providers/api'
import type { ProviderCatalogResponse } from '../../features/providers/types'
import { openExternal } from '../../platform/host'

const PAGE_SIZE = 20
const EMPTY_CATALOG: ProviderCatalogResponse = { total: 0, items: [], page: 1, page_size: PAGE_SIZE }

// 提供商目录没有 logo 资源，用名称首字符做字母标，保持离线可用。
function providerInitial(name: string) {
  const trimmed = name.trim()
  return trimmed ? Array.from(trimmed)[0].toUpperCase() : '?'
}

const headCellClass =
  'border-b border-k-border-soft bg-k-canvas/60 px-4 py-2.5 text-left text-[11px] font-normal tracking-[0.06em] text-k-text-subtle'
const bodyCellClass = 'border-b border-k-border-soft px-4 py-3 align-middle'

export function ProviderCatalogPage() {
  const { message } = App.useApp()
  const [data, setData] = useState<ProviderCatalogResponse>(EMPTY_CATALOG)
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
    fetchProviderCatalogPage(keyword, page, PAGE_SIZE, controller.signal)
      .then(result => { if (!controller.signal.aborted) setData(result) })
      .catch(requestError => {
        if (!controller.signal.aborted) setError(requestError instanceof Error ? requestError.message : '提供商目录加载失败')
      })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
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
    } catch (syncError) {
      void message.error(syncError instanceof Error ? syncError.message : '同步失败')
    } finally {
      setSyncing(false)
    }
  }

  return (
    <div className="mx-auto w-full max-w-[1480px] px-6 py-5 max-[620px]:px-3.5">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="m-0 text-[20px] font-semibold text-k-text">提供商目录</h2>
          <p className="mt-1 mb-0 text-sm text-k-text-muted">
            来自 models.dev 的提供商目录（api.json），按名称 A→Z 排列；新增提供商请在「提供商与模型」中搜索添加
          </p>
        </div>
        <Space wrap>
          <Button icon={<ReloadOutlined />} loading={loading} onClick={() => setRevision(value => value + 1)}>重新加载</Button>
          <Button icon={<SyncOutlined />} loading={syncing} onClick={syncNow}>立即同步</Button>
        </Space>
      </div>

      {error ? <Alert className="mb-3" showIcon type="error" title={error} action={<Button onClick={() => setRevision(value => value + 1)}>重试</Button>} /> : null}

      <Input.Search
        allowClear
        aria-label="检索提供商目录"
        className="mb-3 max-w-[520px]"
        enterButton={<SearchOutlined />}
        onChange={event => setDraftKeyword(event.target.value)}
        onSearch={search}
        placeholder="搜索提供商名称、标识、包名或 API 地址"
        value={draftKeyword}
      />

      <div className="overflow-hidden rounded-xl border border-k-border bg-k-surface">
        <Spin spinning={loading}>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[860px] table-fixed border-collapse text-[13px]">
              <thead>
                <tr>
                  <th className={`${headCellClass} w-[290px]`}>提供商</th>
                  <th className={`${headCellClass} w-[80px]`}>模型</th>
                  <th className={`${headCellClass} w-[240px]`}>包</th>
                  <th className={headCellClass}>API</th>
                  <th className={`${headCellClass} w-[80px] text-center`}>文档</th>
                </tr>
              </thead>
              <tbody className="[&>tr:last-child>td]:border-b-0">
                {data.items.map(item => (
                  <tr className="transition-colors hover:bg-k-selected/40" key={item.id}>
                    <td className={bodyCellClass}>
                      <div className="flex items-center gap-2.5">
                        <span aria-hidden="true" className="grid h-7 w-7 shrink-0 place-items-center rounded-md border border-k-border-soft bg-k-selected font-mono text-[11px] font-semibold uppercase text-k-primary">
                          {providerInitial(item.name)}
                        </span>
                        <span className="min-w-0">
                          <span className="block truncate font-medium text-k-text" title={item.name}>{item.name}</span>
                          <span className="mt-0.5 block truncate font-mono text-[10px] text-k-text-subtle">{item.id}</span>
                        </span>
                      </div>
                    </td>
                    <td className={bodyCellClass}>
                      <span className="font-mono text-[12px] text-k-text-muted">{item.model_count.toLocaleString()}</span>
                    </td>
                    <td className={bodyCellClass}>
                      <span className="block truncate font-mono text-[12px] text-k-text-muted" title={item.npm}>{item.npm || '—'}</span>
                    </td>
                    <td className={bodyCellClass}>
                      <Typography.Text
                        className="block! font-mono text-[12px]!"
                        copyable={{ text: item.api }}
                        ellipsis={{ tooltip: item.api }}
                        type="secondary"
                      >
                        {item.api}
                      </Typography.Text>
                    </td>
                    <td className={`${bodyCellClass} text-center`}>
                      {item.doc ? (
                        <Tooltip title={item.doc}>
                          <Button
                            aria-label={`打开 ${item.name} 文档`}
                            icon={<LinkOutlined />}
                            onClick={() => { void openExternal(item.doc).catch(() => { void message.error('打开文档失败') }) }}
                            size="small"
                            type="text"
                          />
                        </Tooltip>
                      ) : (
                        <span className="text-[11px] text-k-text-subtle">—</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {!loading && data.items.length === 0 ? (
            <div className="py-12">
              <Empty
                description={keyword ? '没有匹配的提供商' : '暂无提供商目录，请先立即同步或到系统配置启用定时同步'}
                image={Empty.PRESENTED_IMAGE_SIMPLE}
              />
            </div>
          ) : null}
          <div className="flex flex-wrap items-center justify-between gap-2 border-t border-k-border-soft px-4 py-2.5">
            <span className="text-[11px] text-k-text-subtle">共 {data.total.toLocaleString()} 个提供商</span>
            <Pagination
              current={page}
              onChange={setPage}
              pageSize={PAGE_SIZE}
              showSizeChanger={false}
              showTotal={total => `共 ${total.toLocaleString()} 个`}
              total={data.total}
            />
          </div>
        </Spin>
      </div>
    </div>
  )
}
