import { useCallback, useEffect, useState } from 'react'
import {
  DownloadOutlined, LockOutlined, PlusOutlined, PushpinOutlined, ReloadOutlined,
} from '@ant-design/icons'
import {
  Alert, App, Button, Card, DatePicker, Descriptions, Drawer, Empty, Form, Input, List,
  Modal, Popconfirm, Select, Space, Spin, Switch, Table, Tabs, Tag, Typography,
} from 'antd'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import dayjs from 'dayjs'
import {
  approveMemoryJob, compileMemory, createMemoryPage, deleteMemoryPage, exportMemory,
  fetchMemoryJobs, fetchMemoryPage, fetchMemoryPageDiff, fetchMemoryPages, fetchMemoryRevisions,
  fetchMemorySource, fetchMemoryStatus, importMemoryDocument, rejectMemoryJob, restoreMemoryRevision,
  retryMemoryJob, startMemoryBackfill, updateMemoryPage,
} from '../../features/memory/api'
import {
  MEMORY_KINDS, MEMORY_STATUSES, basisLabel, statusLabel,
  type MemoryDiff, type MemoryJob, type MemoryPageDetail, type MemoryPageItem, type MemoryRevision,
  type MemorySourceDetail, type MemoryStatus,
} from '../../features/memory/types'
import { fetchProjects } from '../../features/project/api'

const SCOPE_OPTIONS = [
  { value: '', label: '全部范围' },
  { value: 'personal', label: '个人' },
  { value: 'shared', label: '通用' },
]

interface EditorValues {
  scope_key: string
  kind: string
  title: string
  summary: string
  body: string
  aliases: string
  canonical_key: string
  pinned: boolean
  user_locked: boolean
}

interface BackfillValues {
  scope_key: string
  max_sources: number
  range?: [dayjs.Dayjs, dayjs.Dayjs]
}

interface ImportValues {
  scope_key: string
  path: string
}

const diffChangeLabel: Record<string, string> = {
  added: '新增', removed: '移除', changed: '变更',
}

export function MemoryPage() {
  const { message } = App.useApp()
  const [scope, setScope] = useState('')
  const [status, setStatus] = useState('')
  const [keyword, setKeyword] = useState('')
  const [items, setItems] = useState<MemoryPageItem[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(false)
  const [selectedId, setSelectedId] = useState('')
  const [detail, setDetail] = useState<MemoryPageDetail | null>(null)
  const [revisions, setRevisions] = useState<MemoryRevision[]>([])
  const [revisionOpen, setRevisionOpen] = useState(false)
  const [editing, setEditing] = useState<'create' | 'edit' | null>(null)
  const [jobs, setJobs] = useState<MemoryJob[]>([])
  const [memoryStatus, setMemoryStatus] = useState<MemoryStatus | null>(null)
  const [projects, setProjects] = useState<{ id: string; name: string }[]>([])
  const [backfillOpen, setBackfillOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [sourceDetail, setSourceDetail] = useState<MemorySourceDetail | null>(null)
  const [sourceOpen, setSourceOpen] = useState(false)
  const [diff, setDiff] = useState<MemoryDiff | null>(null)
  const [diffOpen, setDiffOpen] = useState(false)
  const [form] = Form.useForm<EditorValues>()
  const [backfillForm] = Form.useForm<BackfillValues>()
  const [importForm] = Form.useForm<ImportValues>()

  const projectScopes = projects.map(project => ({ value: `project:${project.id}`, label: `项目：${project.name}` }))

  const loadList = useCallback(async () => {
    setLoading(true)
    try {
      const list = await fetchMemoryPages({ scope_key: scope || undefined, status: status || undefined, keyword: keyword || undefined })
      setItems(list.items)
      setTotal(list.total)
    } catch (error) {
      message.error(String(error instanceof Error ? error.message : error))
    } finally {
      setLoading(false)
    }
  }, [keyword, message, scope, status])

  const loadDetail = useCallback(async (id: string) => {
    if (!id) {
      setDetail(null)
      return
    }
    try {
      setDetail(await fetchMemoryPage(id))
    } catch (error) {
      message.error(String(error instanceof Error ? error.message : error))
    }
  }, [message])

  const loadJobs = useCallback(async () => {
    try {
      const list = await fetchMemoryJobs()
      setJobs(list.items)
    } catch (error) {
      message.error(String(error instanceof Error ? error.message : error))
    }
  }, [message])

  const loadStatus = useCallback(async () => {
    try {
      setMemoryStatus(await fetchMemoryStatus())
    } catch (error) {
      message.error(String(error instanceof Error ? error.message : error))
    }
  }, [message])

  useEffect(() => { void loadList() }, [loadList])
  useEffect(() => { void loadDetail(selectedId) }, [loadDetail, selectedId])
  useEffect(() => { void loadJobs(); void loadStatus() }, [loadJobs, loadStatus])
  useEffect(() => {
    void (async () => {
      try {
        const page = await fetchProjects('', 1)
        setProjects(page.items.map(item => ({ id: item.id, name: item.name })))
      } catch {
        setProjects([])
      }
    })()
  }, [])

  const submitBackfill = async () => {
    const values = await backfillForm.validateFields()
    try {
      const result = await startMemoryBackfill({
        scope_key: values.scope_key,
        max_sources: values.max_sources,
        after: values.range?.[0]?.startOf('day').toISOString(),
        before: values.range?.[1]?.endOf('day').toISOString(),
      })
      message.success(`历史回填已登记（任务 ${result.job_id}），可在任务与待审中查看进度`)
      setBackfillOpen(false)
      await Promise.all([loadJobs(), loadStatus()])
    } catch (error) {
      message.error(String(error instanceof Error ? error.message : error))
    }
  }

  const submitImport = async () => {
    const values = await importForm.validateFields()
    try {
      const result = await importMemoryDocument({
        scope_key: values.scope_key, path: values.path,
      })
      message.success(result.deduplicated ? '该资料已导入过，沿用已有来源' : '资料已导入并进入整理队列')
      setImportOpen(false)
      await Promise.all([loadJobs(), loadStatus()])
    } catch (error) {
      message.error(String(error instanceof Error ? error.message : error))
    }
  }

  const openSource = async (sourceID: string) => {
    try {
      setSourceDetail(await fetchMemorySource(sourceID))
      setSourceOpen(true)
    } catch (error) {
      message.error(String(error instanceof Error ? error.message : error))
    }
  }

  const openDiff = async (from: number, to?: number) => {
    if (!detail) return
    try {
      setDiff(await fetchMemoryPageDiff(detail.id, from, to))
      setDiffOpen(true)
    } catch (error) {
      message.error(String(error instanceof Error ? error.message : error))
    }
  }

  const openEditor = (mode: 'create' | 'edit') => {
    setEditing(mode)
    if (mode === 'create') {
      form.setFieldsValue({
        scope_key: scope || 'personal', kind: 'fact', title: '', summary: '', body: '',
        aliases: '', canonical_key: '', pinned: false, user_locked: false,
      })
    } else if (detail) {
      form.setFieldsValue({
        scope_key: detail.scope_key, kind: detail.kind, title: detail.title, summary: detail.summary,
        body: detail.body, aliases: detail.aliases.join('、'), canonical_key: '', pinned: detail.pinned,
        user_locked: detail.user_locked,
      })
    }
  }

  const saveEditor = async () => {
    const values = await form.validateFields()
    const aliases = values.aliases.split(/[、,，]/).map(item => item.trim()).filter(Boolean)
    try {
      if (editing === 'create') {
        const created = await createMemoryPage({
          scope_key: values.scope_key, kind: values.kind, title: values.title,
          summary: values.summary, body: values.body, aliases,
          canonical_key: values.canonical_key || undefined,
          pinned: values.pinned, user_locked: values.user_locked,
        })
        message.success('记忆已保存')
        setSelectedId(created.id)
      } else if (detail) {
        const updated = await updateMemoryPage(detail.id, {
          expected_version: detail.version, kind: values.kind, title: values.title,
          summary: values.summary, body: values.body, aliases,
          pinned: values.pinned, user_locked: values.user_locked,
        })
        message.success('记忆已更新')
        setDetail(updated)
      }
      setEditing(null)
      await loadList()
    } catch (error) {
      message.error(String(error instanceof Error ? error.message : error))
    }
  }

  const removePage = async (mode: 'disable' | 'forget') => {
    if (!detail) return
    try {
      await deleteMemoryPage(detail.id, mode)
      message.success(mode === 'forget' ? '记忆已删除' : '记忆已停用')
      if (mode === 'forget') setSelectedId('')
      await Promise.all([loadList(), loadDetail(detail.id), loadStatus()])
    } catch (error) {
      message.error(String(error instanceof Error ? error.message : error))
    }
  }

  const openRevisions = async () => {
    if (!detail) return
    try {
      setRevisions(await fetchMemoryRevisions(detail.id))
      setRevisionOpen(true)
    } catch (error) {
      message.error(String(error instanceof Error ? error.message : error))
    }
  }

  const restore = async (version: number) => {
    if (!detail) return
    try {
      const restored = await restoreMemoryRevision(detail.id, version)
      setDetail(restored)
      setRevisionOpen(false)
      message.success(`已恢复为新版本 v${restored.version}`)
      await loadList()
    } catch (error) {
      message.error(String(error instanceof Error ? error.message : error))
    }
  }

  const exportMarkdown = async () => {
    try {
      const result = await exportMemory(scope ? [scope] : ['personal', 'shared'])
      const blob = new Blob([result.markdown], { type: 'text/markdown;charset=utf-8' })
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = result.filename
      anchor.click()
      URL.revokeObjectURL(url)
    } catch (error) {
      message.error(String(error instanceof Error ? error.message : error))
    }
  }

  const compileNow = async () => {
    if (!scope) {
      message.warning('请先选择要整理的范围')
      return
    }
    try {
      await compileMemory(scope)
      message.success('已进入整理队列，稍后在任务列表查看结果')
      await Promise.all([loadJobs(), loadStatus()])
    } catch (error) {
      message.error(String(error instanceof Error ? error.message : error))
    }
  }

  const runJobAction = async (action: 'approve' | 'reject' | 'retry', id: string) => {
    try {
      if (action === 'approve') await approveMemoryJob(id)
      else if (action === 'reject') await rejectMemoryJob(id)
      else await retryMemoryJob(id)
      await Promise.all([loadJobs(), loadList(), loadStatus()])
    } catch (error) {
      message.error(String(error instanceof Error ? error.message : error))
    }
  }

  const detailPanel = detail ? (
    <Card
      className="min-h-0 flex-1 overflow-auto"
      extra={
        <Space wrap>
          <Button size="small" onClick={() => openEditor('edit')}>编辑</Button>
          <Button size="small" icon={<PushpinOutlined />} type={detail.pinned ? 'primary' : 'default'}
            onClick={async () => {
              const updated = await updateMemoryPage(detail.id, { expected_version: detail.version, pinned: !detail.pinned })
              setDetail(updated)
            }}>置顶</Button>
          <Button size="small" icon={<LockOutlined />} type={detail.user_locked ? 'primary' : 'default'}
            onClick={async () => {
              const updated = await updateMemoryPage(detail.id, { expected_version: detail.version, user_locked: !detail.user_locked })
              setDetail(updated)
            }}>锁定</Button>
          <Button size="small" onClick={openRevisions}>版本</Button>
          {detail.scope_key !== 'shared' && (
            <Popconfirm title="将这一条用于所有对话？内容将对普通与项目对话可见，请确认不包含敏感信息。" onConfirm={async () => {
              const updated = await updateMemoryPage(detail.id, { expected_version: detail.version, scope_key: 'shared' })
              setDetail(updated)
              await loadList()
            }}>
              <Button size="small">用于所有对话</Button>
            </Popconfirm>
          )}
          <Popconfirm title="停用后不再召回，内容保留可恢复" onConfirm={() => removePage('disable')}>
            <Button size="small" disabled={detail.status === 'archived'}>停用</Button>
          </Popconfirm>
          <Popconfirm title="删除记忆将立即清除正文、修订与证据摘录，且不可恢复" onConfirm={() => removePage('forget')}>
            <Button size="small" danger>忘记</Button>
          </Popconfirm>
        </Space>
      }
      title={
        <Space>
          <span>{detail.title}</span>
          <Tag>{statusLabel[detail.status] ?? detail.status}</Tag>
          <Tag color="blue">v{detail.version}</Tag>
        </Space>
      }
    >
      <Descriptions column={2} size="small">
        <Descriptions.Item label="范围">{detail.scope_key}</Descriptions.Item>
        <Descriptions.Item label="类型">{detail.kind}</Descriptions.Item>
        <Descriptions.Item label="别名">{detail.aliases.join('、') || '—'}</Descriptions.Item>
        <Descriptions.Item label="来源数">{detail.source_count}</Descriptions.Item>
      </Descriptions>
      <Typography.Paragraph className="mt-3">{detail.summary}</Typography.Paragraph>
      <ReactMarkdown remarkPlugins={[remarkGfm]}>{detail.body}</ReactMarkdown>
      {detail.claims.length > 0 && (
        <>
          <Typography.Title level={5}>依据</Typography.Title>
          {detail.claims.map(claim => (
            <div key={claim.key} className="mb-2">
              <Space wrap>
                <Tag color="geekblue">{basisLabel[claim.basis] ?? claim.basis}</Tag>
                <span>{claim.statement}</span>
              </Space>
              {claim.evidence.map((evidence, index) => (
                <Typography.Paragraph key={`${claim.key}-${index}`} className="ml-4! mb-0!" type="secondary">
                  {evidence.source}（{evidence.part_key}）：{evidence.quote}
                  <Button className="ml-2!" size="small" type="link" onClick={() => void openSource(evidence.source_id)}>查看来源</Button>
                </Typography.Paragraph>
              ))}
            </div>
          ))}
        </>
      )}
      {detail.related_ids.length > 0 && (
        <Typography.Paragraph type="secondary">关联页面：{detail.related_ids.join('、')}</Typography.Paragraph>
      )}
    </Card>
  ) : (
    <Empty className="m-auto" description="选择左侧记忆查看正文、依据与来源" />
  )

  const pagesTab = (
    <div className="flex h-full min-h-0 gap-3">
      <Card className="w-80 shrink-0 overflow-auto" size="small"
        extra={<Typography.Text type="secondary">{total} 条</Typography.Text>} title="记忆列表">
        <List
          dataSource={items}
          loading={loading}
          locale={{ emptyText: '暂无记忆' }}
          renderItem={(item: MemoryPageItem) => (
            <List.Item className="cursor-pointer!" onClick={() => setSelectedId(item.id)}
              style={item.id === selectedId ? { background: 'rgba(22,119,255,0.08)' } : undefined}>
              <List.Item.Meta
                description={
                  <Space size={4} wrap>
                    <Tag>{statusLabel[item.status] ?? item.status}</Tag>
                    <Tag color="blue">v{item.version}</Tag>
                    {item.pinned && <Tag color="gold">置顶</Tag>}
                    {item.user_locked && <Tag color="red">锁定</Tag>}
                  </Space>
                }
                title={item.title}
              />
            </List.Item>
          )}
        />
      </Card>
      {detailPanel}
    </div>
  )

  const jobsTab = (
    <div className="flex flex-col gap-3">
      <Alert
        message="任务模型负责提炼与整合；编译用量单独统计，不计入聊天口径。失败可重试，待审提案由你决定是否发布。"
        showIcon type="info"
      />
      <Table<MemoryJob>
        columns={[
          { title: '范围', dataIndex: 'scope_key' },
          { title: '状态', dataIndex: 'status', render: (value: string) => <Tag>{value}</Tag> },
          { title: '尝试', dataIndex: 'attempt' },
          { title: '调用', dataIndex: 'calls' },
          { title: '用量', dataIndex: 'total_tokens' },
          {
            title: '进度', key: 'progress', render: (_, row) => row.progress
              ? `扫描 ${row.progress.scanned} / 新增 ${row.progress.created} / 跳过 ${row.progress.skipped}${row.progress.limited ? '（达到上限）' : ''}`
              : '—',
          },
          { title: '错误', dataIndex: 'error_summary', render: (value: string) => value || '—' },
          {
            title: '操作', key: 'actions', render: (_, row) => (
              <Space>
                {row.status === 'needs_review' && (
                  <>
                    <Button size="small" type="primary" onClick={() => void runJobAction('approve', row.id)}>批准</Button>
                    <Button size="small" onClick={() => void runJobAction('reject', row.id)}>拒绝</Button>
                  </>
                )}
                {(row.status === 'failed' || row.status === 'blocked' || row.status === 'canceled') && (
                  <Button size="small" onClick={() => void runJobAction('retry', row.id)}>重试</Button>
                )}
              </Space>
            ),
          },
        ]}
        dataSource={jobs}
        loading={false}
        rowKey="id"
        scroll={{ x: true }}
        size="small"
      />
    </div>
  )

  const statusTab = memoryStatus ? (
    <div className="flex flex-col gap-3">
      <Card size="small" title="记忆状态">
        <Descriptions column={3} size="small">
          <Descriptions.Item label="总开关">{memoryStatus.enabled ? '已开启' : '已关闭'}</Descriptions.Item>
          <Descriptions.Item label="自动捕获">{memoryStatus.auto_capture ? '已开启' : '已关闭（只停止学习，不删除已有页面）'}</Descriptions.Item>
          <Descriptions.Item label="注入上限（估算 token）">{memoryStatus.context_tokens}</Descriptions.Item>
          <Descriptions.Item label="待处理来源">{memoryStatus.pending_sources}</Descriptions.Item>
          <Descriptions.Item label="有效页面">{memoryStatus.active_pages}</Descriptions.Item>
          <Descriptions.Item label="索引版本">normalizer v{memoryStatus.index_normalizer}</Descriptions.Item>
          <Descriptions.Item label="待审任务">{memoryStatus.review_jobs}</Descriptions.Item>
          <Descriptions.Item label="阻塞任务">{memoryStatus.blocked_jobs}</Descriptions.Item>
          <Descriptions.Item label="失败任务">{memoryStatus.failed_jobs}</Descriptions.Item>
        </Descriptions>
      </Card>
      <Card size="small" title="后台任务 / Memory 用量（独立于聊天口径）">
        <Descriptions column={4} size="small">
          <Descriptions.Item label="调用次数">{memoryStatus.memory_calls}</Descriptions.Item>
          <Descriptions.Item label="输入 token">{memoryStatus.memory_input_tokens}</Descriptions.Item>
          <Descriptions.Item label="输出 token">{memoryStatus.memory_output_tokens}</Descriptions.Item>
          <Descriptions.Item label="用量可信">{memoryStatus.memory_usage_known ? '全部已知' : '含未知用量，不按 0 计'}</Descriptions.Item>
        </Descriptions>
      </Card>
    </div>
  ) : <Spin />

  return (
    <div className="flex h-full min-h-0 flex-col gap-3 p-4">
      <Space wrap>
        <Select aria-label="范围" className="w-32" onChange={setScope} options={SCOPE_OPTIONS} value={scope} />
        <Select aria-label="状态" className="w-32" allowClear
          onChange={(value: string | undefined) => setStatus(value ?? '')}
          options={[...MEMORY_STATUSES].map(item => ({ value: item, label: statusLabel[item] }))} placeholder="全部状态" value={status || undefined} />
        <Input.Search allowClear aria-label="搜索" className="w-64" enterButton onSearch={setKeyword} placeholder="搜索标题或摘要" />
        <Button icon={<PlusOutlined />} onClick={() => openEditor('create')} type="primary">保存为记忆</Button>
        <Button onClick={() => void compileNow()}>立即整理</Button>
        <Button onClick={() => { backfillForm.setFieldsValue({ scope_key: scope || 'personal', max_sources: 500 }); setBackfillOpen(true) }}>历史回填</Button>
        <Button onClick={() => { importForm.setFieldsValue({ scope_key: projectScopes[0]?.value ?? '', path: '' }); setImportOpen(true) }}>导入资料</Button>
        <Button icon={<DownloadOutlined />} onClick={() => void exportMarkdown()}>导出 Markdown</Button>
        <Button icon={<ReloadOutlined />} onClick={() => void loadList()}>刷新</Button>
      </Space>
      <Tabs
        className="min-h-0 flex-1"
        items={[
          { key: 'pages', label: '记忆', children: <div className="flex h-full min-h-0">{pagesTab}</div> },
          { key: 'jobs', label: '任务与待审', children: jobsTab },
          { key: 'status', label: '状态与用量', children: statusTab },
        ]}
      />
      <Modal
        confirmLoading={false}
        onCancel={() => setEditing(null)}
        onOk={() => void saveEditor()}
        open={editing !== null}
        title={editing === 'create' ? '保存为记忆' : '编辑记忆'}
        width={720}
      >
        <Form form={form} labelCol={{ span: 4 }} wrapperCol={{ span: 20 }}>
          <Form.Item label="范围" name="scope_key" rules={[{ required: true }]}>
            <Select disabled={editing === 'edit'} options={SCOPE_OPTIONS.filter(item => item.value)} />
          </Form.Item>
          <Form.Item label="类型" name="kind" rules={[{ required: true }]}>
            <Select options={MEMORY_KINDS.map(item => ({ value: item, label: item }))} />
          </Form.Item>
          <Form.Item label="标题" name="title" rules={[{ required: true, max: 120 }]}>
            <Input aria-label="记忆标题" />
          </Form.Item>
          <Form.Item label="摘要" name="summary" rules={[{ max: 300 }]}>
            <Input aria-label="记忆摘要" />
          </Form.Item>
          <Form.Item label="正文" name="body">
            <Input.TextArea aria-label="记忆正文" rows={10} />
          </Form.Item>
          <Form.Item label="别名" name="aliases">
            <Input aria-label="记忆别名" placeholder="用于检索的别名，用、分隔" />
          </Form.Item>
          {editing === 'create' && (
            <Form.Item label="主题键" name="canonical_key">
              <Input aria-label="记忆主题键" placeholder="留空自动从标题派生" />
            </Form.Item>
          )}
          <Form.Item label="置顶" name="pinned" valuePropName="checked">
            <Switch aria-label="置顶" />
          </Form.Item>
          <Form.Item label="锁定" name="user_locked" valuePropName="checked">
            <Switch aria-label="锁定" />
          </Form.Item>
        </Form>
      </Modal>
      <Drawer
        extra={
          <Popconfirm title="以该内容新建恢复修订（不回退版本号）？" onConfirm={() => void restore(Number(revisions[0]?.version))}>
            <Button disabled={revisions.length === 0}>恢复最新历史</Button>
          </Popconfirm>
        }
        onClose={() => setRevisionOpen(false)}
        open={revisionOpen}
        title="版本历史"
        width={560}
      >
        <List
          dataSource={revisions}
          locale={{ emptyText: '暂无修订' }}
          renderItem={(item: MemoryRevision) => (
            <List.Item
              actions={[
                <Button key="diff" size="small" onClick={() => void openDiff(item.version, detail?.version)}>对比当前</Button>,
                <Button key="restore" size="small" onClick={() => void restore(item.version)}>恢复</Button>,
              ]}
            >
              <List.Item.Meta
                description={
                  <Space direction="vertical" size={0}>
                    <Typography.Text type="secondary">
                      {item.actor === 'user' ? '用户' : item.actor === 'task_model' ? '任务模型' : '系统'} · {item.reason || '—'}
                    </Typography.Text>
                    <Typography.Text type="secondary">{item.summary || item.body.slice(0, 60)}</Typography.Text>
                  </Space>
                }
                title={<Space><Tag color="blue">v{item.version}</Tag>{item.title}</Space>}
              />
            </List.Item>
          )}
        />
      </Drawer>
      <Modal
        onCancel={() => setBackfillOpen(false)}
        onOk={() => void submitBackfill()}
        open={backfillOpen}
        title="历史回填"
      >
        <Alert className="mb-3" message="显式选择范围与成本上限；私密/只读会话与已删除会话不参与。回填按页扫描，可中断并幂等重跑。" showIcon type="info" />
        <Form form={backfillForm} labelCol={{ span: 6 }} wrapperCol={{ span: 18 }}>
          <Form.Item label="范围" name="scope_key" rules={[{ required: true }]}>
            <Select options={[{ value: 'personal', label: '个人' }, ...projectScopes]} />
          </Form.Item>
          <Form.Item label="最多新增来源" name="max_sources" rules={[{ required: true }]}>
            <Input aria-label="最多新增来源" type="number" />
          </Form.Item>
          <Form.Item label="时间范围（可选）" name="range">
            <DatePicker.RangePicker aria-label="回填时间范围" />
          </Form.Item>
        </Form>
      </Modal>
      <Modal
        onCancel={() => setImportOpen(false)}
        onOk={() => void submitImport()}
        open={importOpen}
        title="导入项目资料"
      >
        <Alert className="mb-3" message="复用项目路径校验、忽略规则与大小限制；同路径同内容重复导入不会产生新来源。" showIcon type="info" />
        <Form form={importForm} labelCol={{ span: 6 }} wrapperCol={{ span: 18 }}>
          <Form.Item label="项目范围" name="scope_key" rules={[{ required: true }]}>
            <Select options={projectScopes} />
          </Form.Item>
          <Form.Item label="项目内路径" name="path" rules={[{ required: true }]}>
            <Input aria-label="资料路径" placeholder="例如 docs/design.md" />
          </Form.Item>
        </Form>
      </Modal>
      <Drawer
        onClose={() => setSourceOpen(false)}
        open={sourceOpen}
        title="来源导航"
        width={620}
      >
        {sourceDetail && (
          <Space className="w-full" direction="vertical" size="middle">
            <Descriptions column={1} size="small">
              <Descriptions.Item label="类型">{sourceDetail.kind}</Descriptions.Item>
              <Descriptions.Item label="状态">{sourceDetail.state}</Descriptions.Item>
              <Descriptions.Item label="范围">{sourceDetail.scope_key}</Descriptions.Item>
              {sourceDetail.conversation_id && <Descriptions.Item label="会话">{sourceDetail.conversation_id}</Descriptions.Item>}
              {sourceDetail.turn_id && <Descriptions.Item label="轮次">{sourceDetail.turn_id}</Descriptions.Item>}
              {sourceDetail.turn_status && <Descriptions.Item label="轮次状态">{sourceDetail.turn_status}{sourceDetail.finish_reason ? ` / ${sourceDetail.finish_reason}` : ''}</Descriptions.Item>}
              {sourceDetail.document_path && <Descriptions.Item label="资料路径">{sourceDetail.document_path}</Descriptions.Item>}
            </Descriptions>{" "}
            {!sourceDetail.available && (
              <Alert message={sourceDetail.unavailable_reason || '来源不可用'} showIcon type="warning" />
            )}
            {sourceDetail.parts.map(part => (
              <Card key={part.part_key} size="small" title={`${part.part_key}（${part.origin}）`}>
                <Typography.Paragraph className="whitespace-pre-wrap">
                  {part.text}{part.truncated ? '…（已截断）' : ''}
                </Typography.Paragraph>
              </Card>
            ))}
          </Space>
        )}
      </Drawer>
      <Modal footer={null} onCancel={() => setDiffOpen(false)} open={diffOpen} title="版本对比" width={720}>
        {diff && (
          <Space className="w-full" direction="vertical" size="middle">
            <Descriptions column={2} size="small">
              <Descriptions.Item label="从">v{diff.from.version}（{diff.from.actor}，{diff.from.created_at}）</Descriptions.Item>
              <Descriptions.Item label="到">v{diff.to.version}（{diff.to.actor}，{diff.to.created_at}）</Descriptions.Item>
            </Descriptions>
            {[['内容变化', diff.content_changes], ['元数据变化', diff.metadata_changes]].map(([title, changes]) => (
              <Card key={String(title)} size="small" title={String(title)}>
                {(changes as typeof diff.content_changes).length === 0 ? <Typography.Text type="secondary">无</Typography.Text> : (
                  <List
                    dataSource={changes as typeof diff.content_changes}
                    renderItem={change => (
                      <List.Item>
                        <Typography.Text strong>{change.field}</Typography.Text>：{change.from || '（空）'} → {change.to || '（空）'}
                      </List.Item>
                    )}
                  />
                )}
              </Card>
            ))}
            <Card size="small" title="主张变化">
              {diff.claim_changes.length === 0 ? <Typography.Text type="secondary">无</Typography.Text> : (
                <List
                  dataSource={diff.claim_changes}
                  renderItem={change => (
                    <List.Item>
                      <Space wrap>
                        <Tag>{diffChangeLabel[change.change] ?? change.change}</Tag>
                        <Typography.Text strong>{change.key}</Typography.Text>
                        <span>{change.from || '—'} → {change.to || '—'}</span>
                      </Space>
                    </List.Item>
                  )}
                />
              )}
            </Card>
            <Card size="small" title="证据变化">
              {diff.evidence_changes.length === 0 ? <Typography.Text type="secondary">无</Typography.Text> : (
                <List
                  dataSource={diff.evidence_changes}
                  renderItem={change => (
                    <List.Item>
                      <Space wrap>
                        <Tag>{diffChangeLabel[change.change] ?? change.change}</Tag>
                        <Typography.Text strong>{change.claim_key}</Typography.Text>
                        <Typography.Text type="secondary">{change.source_id}/{change.part_key}</Typography.Text>
                        {change.quote && <span>{change.quote}</span>}
                      </Space>
                    </List.Item>
                  )}
                />
              )}
            </Card>
          </Space>
        )}
      </Modal>
    </div>
  )
}
