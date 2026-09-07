import type { ReactNode } from 'react'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
import {
  ClockCircleOutlined,
  DatabaseOutlined,
  DesktopOutlined,
  GlobalOutlined,
  ReloadOutlined,
  SearchOutlined,
  UndoOutlined,
} from '@ant-design/icons'
import {
  Alert,
  Button,
  Card,
  DatePicker,
  Empty,
  Form,
  Input,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
} from 'antd'
import type { TableColumnsType } from 'antd'
import {
  DEFAULT_ACCESS_LOG_QUERY,
  useAccessLogs,
} from '../../features/access-logs/useAccessLogs'
import type {
  AccessLog,
  AccessLogQuery,
} from '../../features/access-logs/types'

const { RangePicker } = DatePicker

interface FilterFormValues {
  accessIP?: string
  accessTime?: [Dayjs, Dayjs]
}

interface SummaryCardProps {
  detail: string
  icon: ReactNode
  iconClassName: string
  label: string
  value: string
  valueClassName?: string
}

function formatAccessTime(timestamp?: number) {
  if (!timestamp || !Number.isFinite(timestamp)) {
    return '—'
  }

  const value = timestamp > 10_000_000_000 ? timestamp : timestamp * 1000
  return dayjs(value).format('YYYY-MM-DD HH:mm:ss')
}

function formatClient(name?: string, version?: string) {
  if (!name) {
    return '—'
  }

  return version ? `${name} ${version}` : name
}

function SummaryCard({
  detail,
  icon,
  iconClassName,
  label,
  value,
  valueClassName = 'text-xl',
}: SummaryCardProps) {
  return (
    <Card className="border-k-border! bg-k-surface! shadow-sm shadow-black/5">
      <div className="flex min-h-[68px] items-center gap-3.5">
        <div
          className={`grid h-10 w-10 shrink-0 place-items-center rounded-xl border text-base ${iconClassName}`}
        >
          {icon}
        </div>
        <div className="min-w-0">
          <span className="mb-1 block text-[10px] tracking-[0.2px] text-k-text-muted">
            {label}
          </span>
          <strong
            className={`block min-h-[25px] truncate font-semibold leading-[25px] text-k-text ${valueClassName}`}
          >
            {value}
          </strong>
          <small className="mt-0.5 block text-[9px] text-k-text-subtle">
            {detail}
          </small>
        </div>
      </div>
    </Card>
  )
}

const columns: TableColumnsType<AccessLog> = [
  {
    title: '访问时间',
    dataIndex: 'access_time',
    key: 'access_time',
    width: 176,
    render: (value: number) => (
      <span className="inline-flex items-center gap-2 whitespace-nowrap font-mono text-[10.5px] text-k-text-muted">
        <ClockCircleOutlined className="text-k-text-subtle" />
        {formatAccessTime(value)}
      </span>
    ),
  },
  {
    title: '访问 IP',
    dataIndex: 'access_ip',
    key: 'access_ip',
    width: 150,
    render: (value: string) => (
      <span className="inline-flex items-center gap-2 whitespace-nowrap font-mono text-[11px] text-k-primary">
        <GlobalOutlined className="opacity-70" />
        {value || '—'}
      </span>
    ),
  },
  {
    title: '操作系统',
    dataIndex: 'os',
    key: 'os',
    width: 150,
    render: (value?: string) => (
      <Tag
        className="m-0! max-w-[130px] overflow-hidden text-ellipsis rounded-full! border-k-border! bg-k-elevated! text-[10px]! text-k-text-muted!"
        icon={<DesktopOutlined />}
      >
        {value || '未知'}
      </Tag>
    ),
  },
  {
    title: '平台',
    dataIndex: 'platform',
    key: 'platform',
    width: 130,
    render: (value?: string) => value || '—',
  },
  {
    title: '浏览器',
    key: 'browser',
    width: 190,
    render: (_, record) => (
      <Tooltip title={formatClient(record.browser_name, record.browser_version)}>
        <span className="block truncate text-k-text-muted">
          {formatClient(record.browser_name, record.browser_version)}
        </span>
      </Tooltip>
    ),
  },
  {
    title: '浏览器引擎',
    key: 'engine',
    width: 180,
    render: (_, record) => (
      <Tooltip
        title={formatClient(
          record.browser_engine_name,
          record.browser_engine_version,
        )}
      >
        <span className="block truncate text-k-text-muted">
          {formatClient(
            record.browser_engine_name,
            record.browser_engine_version,
          )}
        </span>
      </Tooltip>
    ),
  },
  {
    title: '日志 ID',
    dataIndex: 'id',
    key: 'id',
    width: 190,
    render: (value: string) => (
      <Typography.Text
        className="inline-flex w-[158px] items-center font-mono text-[10px]! text-k-text-subtle!"
        copyable={{ text: value, tooltips: ['复制 ID', '已复制'] }}
        ellipsis={{ tooltip: value }}
      >
        {value}
      </Typography.Text>
    ),
  },
]

export function AccessLogPage() {
  const [form] = Form.useForm<FilterFormValues>()
  const { data, error, loading, query, reload, setQuery } = useAccessLogs()

  const submitFilters = (values: FilterFormValues) => {
    const nextQuery: AccessLogQuery = {
      page: 1,
      pageSize: query.pageSize,
    }
    const accessIP = values.accessIP?.trim()

    if (accessIP) {
      nextQuery.accessIP = accessIP
    }
    if (values.accessTime?.[0]) {
      nextQuery.startTime = values.accessTime[0].unix()
    }
    if (values.accessTime?.[1]) {
      nextQuery.endTime = values.accessTime[1].unix()
    }

    setQuery(nextQuery)
  }

  const resetFilters = () => {
    form.resetFields()
    setQuery({ ...DEFAULT_ACCESS_LOG_QUERY })
  }

  const latestAccess = data.items[0]?.access_time
  const totalPages = data.total ? Math.ceil(data.total / query.pageSize) : 0
  const isFirstLoading = loading && data.items.length === 0 && !error

  return (
    <div className="mx-auto w-full max-w-[1480px] px-8 pt-7 pb-11 max-[900px]:px-5 max-[620px]:px-3.5 max-[620px]:pt-5">
      <div className="mb-5 flex items-end justify-between gap-6 max-[620px]:items-start">
        <div>
          <span className="mb-2 block text-[9px] font-bold tracking-[1.4px] text-k-text-subtle">
            SYSTEM / AUDIT
          </span>
          <h2 className="m-0 text-[22px] font-semibold tracking-[-0.25px] text-k-text">
            访问日志
          </h2>
          <p className="mt-1.5 mb-0 text-xs text-k-text-muted max-[620px]:max-w-[310px] max-[620px]:leading-5">
            查询并审计 Kaguya API 的访问来源、客户端与访问时间。
          </p>
        </div>
        <Button icon={<ReloadOutlined />} loading={loading} onClick={reload}>
          刷新
        </Button>
      </div>

      <div className="mb-3.5 grid grid-cols-3 gap-3 max-[1000px]:grid-cols-1">
        <SummaryCard
          detail="当前查询条件下的总数"
          icon={<DatabaseOutlined />}
          iconClassName="border-blue-200 bg-blue-50 text-blue-600 dark:border-[#2d4564] dark:bg-[#17263a] dark:text-[#8cbcff]"
          label="匹配记录"
          value={isFirstLoading ? '—' : data.total.toLocaleString()}
        />
        <SummaryCard
          detail={`本页 ${data.items.length} 条记录`}
          icon={<GlobalOutlined />}
          iconClassName="border-emerald-200 bg-emerald-50 text-emerald-600 dark:border-[#2a4c3a] dark:bg-[#152a20] dark:text-[#70dca1]"
          label="当前分页"
          value={totalPages ? `${query.page} / ${totalPages}` : '—'}
        />
        <SummaryCard
          detail="按访问时间倒序排列"
          icon={<ClockCircleOutlined />}
          iconClassName="border-amber-200 bg-amber-50 text-amber-600 dark:border-[#534128] dark:bg-[#2d2417] dark:text-[#f6c679]"
          label="最近访问"
          value={isFirstLoading ? '—' : formatAccessTime(latestAccess)}
          valueClassName="font-mono! text-sm"
        />
      </div>

      <Card className="mb-3.5 border-k-border! bg-k-surface! shadow-sm shadow-black/5">
        <div className="mb-3.5">
          <h3 className="m-0 text-[13px] font-semibold text-k-text">查询条件</h3>
          <p className="mt-1 mb-0 text-[10px] text-k-text-subtle">
            可按访问 IP 与时间范围筛选日志
          </p>
        </div>
        <Form
          form={form}
          layout="vertical"
          onFinish={submitFilters}
          requiredMark={false}
        >
          <div className="grid grid-cols-[minmax(220px,0.8fr)_minmax(380px,1.4fr)_auto] items-end gap-3.5 max-[1100px]:grid-cols-2 max-[620px]:grid-cols-1">
            <Form.Item className="mb-0!" label="访问 IP" name="accessIP">
              <Input
                allowClear
                maxLength={64}
                placeholder="例如：192.168.1.10"
                prefix={<GlobalOutlined className="text-k-text-subtle" />}
              />
            </Form.Item>
            <Form.Item className="mb-0!" label="访问时间" name="accessTime">
              <RangePicker
                allowClear
                className="w-full"
                format="YYYY-MM-DD HH:mm:ss"
                placeholder={['开始时间', '结束时间']}
                showTime
              />
            </Form.Item>
            <div className="flex items-center justify-end gap-2 max-[1100px]:col-span-2 max-[620px]:col-span-1 max-[620px]:[&_.ant-btn]:flex-1">
              <Button icon={<UndoOutlined />} onClick={resetFilters}>
                重置
              </Button>
              <Button
                htmlType="submit"
                icon={<SearchOutlined />}
                loading={loading}
                type="primary"
              >
                查询
              </Button>
            </div>
          </div>
        </Form>
      </Card>

      {error ? (
        <Alert
          action={
            <Button danger ghost onClick={reload} size="small">
              重新加载
            </Button>
          }
          className="mb-3.5"
          description={error}
          message="访问日志加载失败"
          showIcon
          type="error"
        />
      ) : null}

      <Card
        className="overflow-hidden border-k-border! bg-k-surface! shadow-sm shadow-black/5"
        styles={{ body: { padding: 0 } }}
      >
        <div className="flex min-h-16 items-center justify-between border-b border-k-border-soft px-4 py-3.5">
          <div>
            <h3 className="m-0 text-[13px] font-semibold text-k-text">访问记录</h3>
            <p className="mt-1 mb-0 text-[10px] text-k-text-subtle">
              数据来自 SystemAccessLogPage
            </p>
          </div>
          <Space size={8}>
            <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 shadow-[0_0_0_3px_rgb(16_185_129_/_8%)]" />
            <span className="text-[10px] text-k-text-subtle">
              {loading ? '正在同步' : `共 ${data.total.toLocaleString()} 条`}
            </span>
          </Space>
        </div>
        <Table<AccessLog>
          columns={columns}
          dataSource={data.items}
          loading={loading}
          locale={{
            emptyText: (
              <Empty
                description="暂无符合条件的访问日志"
                image={Empty.PRESENTED_IMAGE_SIMPLE}
              />
            ),
          }}
          onChange={(pagination) => {
            const pageSize = pagination.pageSize ?? query.pageSize
            const page =
              pageSize === query.pageSize ? (pagination.current ?? 1) : 1

            setQuery((currentQuery) => ({
              ...currentQuery,
              page,
              pageSize,
            }))
          }}
          pagination={{
            current: query.page,
            pageSize: query.pageSize,
            pageSizeOptions: [10, 20, 50, 100],
            showQuickJumper: data.total > 100,
            showSizeChanger: true,
            showTotal: (total, range) =>
              `${range[0]}-${range[1]} / 共 ${total} 条`,
            total: data.total,
          }}
          rowKey="id"
          scroll={{ x: 1166 }}
          size="middle"
        />
      </Card>
    </div>
  )
}
