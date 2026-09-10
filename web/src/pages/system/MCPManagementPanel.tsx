import { useEffect, useState } from 'react'
import { Alert, App, Button, Card, Drawer, Empty, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import { PlusOutlined, QuestionCircleOutlined, ReloadOutlined } from '@ant-design/icons'
import { createMCPServer, deleteMCPServer, fetchMCPServer, fetchMCPServers, setMCPEnabled, updateMCPServer } from '../../features/mcp/api'
import { MCPJSONEditor } from '../../features/mcp/MCPJSONEditor'
import { parseArgs, parseStringMap, toMCPConfig, toMCPForm } from '../../features/mcp/form'
import type { MCPFormValues } from '../../features/mcp/form'
import type { MCPPage, MCPServer } from '../../features/mcp/types'

const transports = [
  { value: 'streamable-http', label: 'Streamable HTTP' },
  { value: 'stdio', label: 'stdio（本地进程）' },
  { value: 'sse', label: 'SSE（兼容旧服务）' },
]

export function MCPManagementPanel() {
  const { message } = App.useApp()
  const [form] = Form.useForm<MCPFormValues>()
  const transport = Form.useWatch('transport', form)
  const [query, setQuery] = useState({ page: 1, pageSize: 10, name: '' })
  const [data, setData] = useState<MCPPage>({ items: [], total: 0, page: 1, page_size: 10 })
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  const [busy, setBusy] = useState('')
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<MCPServer>()
  const [details, setDetails] = useState<MCPServer>()
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout>
    const load = async (initial: boolean) => {
      if (initial) setLoading(true)
      try {
        const page = await fetchMCPServers(query.page, query.pageSize, query.name, controller.signal)
        if (!controller.signal.aborted) { setData(page); setError('') }
      } catch (err) {
        if (!controller.signal.aborted) setError(err instanceof Error ? err.message : 'MCP 列表加载失败')
      } finally {
        if (!controller.signal.aborted) { setLoading(false); timer = setTimeout(() => void load(false), 5000) }
      }
    }
    void load(true)
    return () => { controller.abort(); clearTimeout(timer) }
  }, [query, revision])

  const reload = () => setRevision(value => value + 1)
  const edit = async (server: MCPServer) => {
    setBusy(server.id)
    try {
      const detail = await fetchMCPServer(server.id)
      setEditing(detail); form.setFieldsValue(toMCPForm(detail)); setOpen(true)
    } catch (err) { void message.error(err instanceof Error ? err.message : '详情加载失败') }
    finally { setBusy('') }
  }
  const save = async () => {
    try {
      const values = await form.validateFields()
      const config = toMCPConfig(values)
      setSaving(true)
      if (editing) await updateMCPServer(editing.id, config)
      else await createMCPServer(config)
      void message.success(editing ? 'MCP 配置已更新' : 'MCP 已创建，可通过开关启用')
      setOpen(false); reload()
    } catch (err) { if (err instanceof Error) void message.error(err.message) }
    finally { setSaving(false) }
  }
  const toggle = async (server: MCPServer, enabled: boolean) => {
    setBusy(server.id)
    try { await setMCPEnabled(server.id, enabled); void message.success(enabled ? 'MCP 已启用，新对话轮次可使用工具' : 'MCP 已停用') }
    catch (err) { void message.error(err instanceof Error ? err.message : 'MCP 启停失败') }
    finally { setBusy(''); reload() }
  }
  const remove = async (server: MCPServer) => {
    setBusy(server.id)
    try {
      await deleteMCPServer(server.id)
      void message.success('MCP 已删除')
      if (data.items.length === 1 && query.page > 1) setQuery({ ...query, page: query.page - 1 })
      else reload()
    } catch (err) { void message.error(err instanceof Error ? err.message : '删除失败') }
    finally { setBusy('') }
  }

  return <div className="mx-auto w-full max-w-[1480px] px-8 pt-4 pb-11 max-[620px]:px-3.5">
    <div className="mb-5 flex flex-wrap items-center justify-between gap-3">
      <div><h2 className="m-0 text-[22px] font-semibold text-k-text">MCP 管理</h2><Typography.Text type="secondary">连接外部工具服务，动态启停，无需重启应用。</Typography.Text></div>
      <Space><Button icon={<ReloadOutlined />} onClick={reload} loading={loading}>刷新</Button><Button type="primary" icon={<PlusOutlined />} disabled={!!busy || saving} onClick={() => { setEditing(undefined); form.resetFields(); form.setFieldsValue(toMCPForm()); setOpen(true) }}>新增 MCP</Button></Space>
    </div>
    <Card>
      <Input.Search aria-label="搜索 MCP 名称" placeholder="搜索 MCP 名称" allowClear className="mb-4 max-w-[360px]" onSearch={name => setQuery({ ...query, name: name.trim(), page: 1 })} />
      {error && <Alert type="error" title={error} showIcon className="mb-4" />}
      <Table<MCPServer> rowKey="id" dataSource={data.items} loading={loading} scroll={{ x: 900 }}
        pagination={{ current: query.page, pageSize: query.pageSize, total: data.total, showSizeChanger: true, onChange: (page, pageSize) => setQuery({ ...query, page, pageSize }) }}
        columns={[
          { title: '名称', dataIndex: 'name', width: 180, render: (name, server) => <Button type="link" className="px-0!" onClick={() => setDetails(server)}>{name}</Button> },
          { title: '传输', dataIndex: 'transport', width: 150 },
          { title: '运行状态', width: 200, render: (_, server) => <div><Tag color={server.status.state === 'running' ? 'green' : server.status.state === 'error' ? 'red' : 'default'}>{server.status.state === 'running' ? '运行中' : server.status.state === 'error' ? '连接异常' : server.enabled ? '等待连接' : '已停用'}</Tag>{server.status.message && <div className="mt-1 text-xs text-k-text-muted">{server.status.message}</div>}</div> },
          { title: '工具数', width: 70, render: (_, server) => server.status.tools.length },
          { title: <Space size={4}>启用<Tooltip trigger={['hover', 'focus']} title="启用的 MCP 工具对所有聊天生效；新启用的工具从下一轮对话可用。停用会中断正在执行的 MCP 请求，已产生的外部操作不会撤销。"><QuestionCircleOutlined tabIndex={0} aria-label="启停说明" className="text-k-text-muted" /></Tooltip></Space>, width: 90, render: (_, server) => <Switch aria-label={`启用 ${server.name}`} checked={server.enabled} loading={busy === server.id} disabled={!!busy || saving} onChange={enabled => void toggle(server, enabled)} /> },
          { title: '操作', width: 160, fixed: 'right', render: (_, server) => <Space size={0}><Button type="link" disabled={!!busy || saving} onClick={() => void edit(server)}>编辑</Button>{server.status.state === 'error' && <Button type="link" disabled={!!busy || saving} onClick={() => void toggle(server, true)}>重试</Button>}<Popconfirm title={`删除 ${server.name}？`} description="运行中的连接会同时关闭。" okText="删除" cancelText="取消" onConfirm={() => remove(server)}><Button danger type="link" disabled={!!busy || saving}>删除</Button></Popconfirm></Space> },
        ]} />
    </Card>
    <Modal title={<Space>{editing ? '编辑 MCP' : '新增 MCP'}{editing?.enabled && <Tooltip trigger={['hover', 'focus']} title="保存时会连接新配置；成功后替换原连接，失败时保留原配置。"><QuestionCircleOutlined tabIndex={0} aria-label="保存说明" className="text-k-text-muted" /></Tooltip>}</Space>} open={open} width={680} onCancel={() => { if (!saving) setOpen(false) }} onOk={() => void save()} confirmLoading={saving} cancelButtonProps={{ disabled: saving }} mask={{ closable: !saving }} closable={!saving} keyboard={!saving} okText="保存" cancelText="取消" destroyOnHidden>
      <Form form={form} layout="vertical" disabled={saving} preserve={false}>
        <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true, message: '请输入名称' }]}><Input aria-label="MCP 名称" maxLength={100} /></Form.Item>
        <Form.Item name="transport" label="传输方式" rules={[{ required: true }]}><Select options={transports} /></Form.Item>
        {transport === 'stdio' ? <>
          <Form.Item name="command" label="可执行文件" tooltip="命令在 Kaguya 所在主机以服务进程权限执行。请填写可执行文件，参数单独填写；不会通过 shell 解析命令。" rules={[{ required: true, whitespace: true, message: '请输入可执行文件' }]}><Input placeholder="例如 npx 或 /usr/local/bin/uvx" /></Form.Item>
          <Form.Item name="args_json" label="命令参数" tooltip="按顺序填写 JSON 字符串数组，每个元素对应一个参数。"><MCPJSONEditor label="命令参数" kind="字符串数组" parse={parseArgs} disabled={saving} placeholder={'[\n  "-y",\n  "@example/mcp-server"\n]'} /></Form.Item>
          <Form.Item name="working_directory" label="工作目录（绝对路径，可选）"><Input placeholder="留空使用 Kaguya 服务工作目录" /></Form.Item>
          <Form.Item name="env_json" label="环境变量" tooltip="继承服务环境，同名变量以此处为准。"><MCPJSONEditor label="环境变量" kind="字符串对象" parse={value => parseStringMap(value, '环境变量')} disabled={saving} /></Form.Item>
        </> : <>
          <Form.Item name="url" label="MCP URL" rules={[{ required: true, whitespace: true, message: '请输入 MCP URL' }, { type: 'url', message: '请输入完整 HTTP(S) URL' }]}><Input placeholder="https://example.com/mcp" /></Form.Item>
          <Form.Item name="headers_json" label="HTTP 请求头" tooltip="例如 Authorization，可用于 Bearer Token 或 API Key 认证。"><MCPJSONEditor label="HTTP 请求头" kind="字符串对象" parse={value => parseStringMap(value, '请求头')} disabled={saving} placeholder={'{\n  "Authorization": "Bearer ..."\n}'} /></Form.Item>
        </>}
        <Form.Item name="timeout_seconds" label="工具调用超时（秒）" tooltip="连接及工具发现最多等待 15 秒。" rules={[{ required: true }]}><InputNumber min={1} max={600} /></Form.Item>
      </Form>
    </Modal>
    <Drawer title={details?.name} open={!!details} onClose={() => setDetails(undefined)}>
      {details && <><Typography.Paragraph>传输方式：{details.transport}</Typography.Paragraph><Typography.Paragraph>工具调用超时：{details.timeout_seconds} 秒</Typography.Paragraph><Typography.Paragraph>创建时间：{new Date(details.created_at).toLocaleString()}</Typography.Paragraph><Typography.Title level={5}>已发现工具</Typography.Title>{details.status.tools.length ? <Space wrap>{details.status.tools.map(name => <Tag key={name}>{name}</Tag>)}</Space> : <Empty description="启用后展示可用工具" image={Empty.PRESENTED_IMAGE_SIMPLE} />}</>}
    </Drawer>
  </div>
}
