import { useEffect, useMemo, useState } from 'react'
import { Alert, Button, Card, DatePicker, Empty, Segmented, Spin, Statistic, theme } from 'antd'
import dayjs from 'dayjs'
import type { EChartsCoreOption } from 'echarts/core'
import { fetchTokenUsage, type TokenUsage } from '../../features/usage/api'
import { aggregateActivity, activityPieces, type ActivityMode } from '../../features/usage/activity'
import { UsageChart } from '../../features/usage/UsageChart'
import { usageDateRange } from '../../features/usage/range'
import './token-usage.css'

const compact = (value: number) => new Intl.NumberFormat('zh-CN', { notation: 'compact', maximumFractionDigits: 1 }).format(value)
// 热力图分档颜色：由浅到深，最低档也保持不透明，确保有使用的日期不会看起来像空白。
const activityColors = ['#c6ddff', '#82b8ff', '#448cef', '#245aca']

export function TokenUsagePage() {
  const { token } = theme.useToken()
  const [data, setData] = useState<TokenUsage>()
  const [range, setRange] = useState<[string, string]>()
  const [mode, setMode] = useState<ActivityMode>('daily')
  const [dimension, setDimension] = useState('model')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError('')
    setData(undefined)
    const timestamps = range ? usageDateRange(...range) : undefined
    fetchTokenUsage(timestamps?.[0], timestamps?.[1], controller.signal).then(result => {
      if (!controller.signal.aborted) setData(result)
    }).catch(error => {
      if (!controller.signal.aborted) setError(error instanceof Error ? error.message : '用量分析加载失败')
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [range, revision])

  const activity = useMemo<EChartsCoreOption>(() => {
    if (!data) return {}
    const values = aggregateActivity(data.days, mode)
    // 分位数分档：值差异极大时低用量日期仍保留可见颜色，并与空白日期区分。
    const pieces = activityPieces(values.map(([, value]) => value), activityColors, compact)
    const base = {
      animation: false, aria: { enabled: true }, textStyle: { color: token.colorTextSecondary },
      tooltip: { trigger: 'item', renderMode: 'richText', backgroundColor: token.colorBgElevated, textStyle: { color: token.colorText },
        formatter: (params: { data: (string | number)[] }) => {
          const [key, value] = mode === 'daily' ? params.data : [values[Number(params.data[0])]?.[0], params.data[2]]
          return `${key}${mode === 'weekly' ? ' 当周' : ''}\nToken：${Number(value).toLocaleString()}`
        } },
      visualMap: { type: 'piecewise', pieces, orient: 'horizontal', left: 'center', bottom: 0, itemWidth: 14, itemHeight: 10, textStyle: { color: token.colorTextSecondary } },
    }
    return mode === 'daily' ? { ...base,
      calendar: { range: [data.activity_start, data.activity_end], top: 35, left: 42, right: 16, bottom: 60, cellSize: ['auto', 22], splitLine: { show: false }, yearLabel: { show: false }, dayLabel: { firstDay: 1, nameMap: ['日', '一', '二', '三', '四', '五', '六'], color: token.colorTextSecondary }, monthLabel: { nameMap: 'cn', color: token.colorTextSecondary }, itemStyle: { borderWidth: 3, borderColor: token.colorBgContainer } },
      series: [{ type: 'heatmap', coordinateSystem: 'calendar', data: values }],
    } : { ...base,
      grid: { top: 45, left: 25, right: 25, bottom: 100 },
      xAxis: { type: 'category', data: values.map(([key]) => key), axisLine: { show: false }, axisTick: { show: false }, axisLabel: { color: token.colorTextSecondary, formatter: (value: string) => mode === 'monthly' ? value : value.slice(5) } },
      yAxis: { type: 'category', data: [''], show: false },
      series: [{ type: 'heatmap', data: values.map(([, value], index) => [index, 0, value]), itemStyle: { borderColor: token.colorBgContainer, borderWidth: 4, borderRadius: 5 } }],
    }
  }, [data, mode, token])

  const composition = useMemo<EChartsCoreOption>(() => {
    const rows = (dimension === 'model' ? data?.models : data?.providers) ?? []
    return {
      aria: { enabled: true }, color: ['#80b2fa', '#2862ce', '#4a8de5', '#c2dbff'],
      tooltip: { trigger: 'axis', renderMode: 'richText', axisPointer: { type: 'shadow' }, backgroundColor: token.colorBgElevated, textStyle: { color: token.colorText } },
      legend: { top: 0, textStyle: { color: token.colorTextSecondary }, icon: 'roundRect' },
      grid: { left: 65, right: 20, top: 45, bottom: 80 },
      xAxis: { type: 'category', data: rows.map(row => dimension === 'model' ? `${row.provider_name}\n${row.name || row.id}` : row.name || row.id), axisTick: { show: false }, axisLine: { lineStyle: { color: token.colorBorderSecondary } }, axisLabel: { interval: 0, width: 110, overflow: 'truncate', color: token.colorTextSecondary } },
      yAxis: { type: 'value', axisLabel: { formatter: compact, color: token.colorTextSecondary }, splitLine: { lineStyle: { color: token.colorBorderSecondary, type: 'dashed' } } },
      series: ([['输入', 'input_tokens'], ['输出', 'output_tokens'], ['思考', 'reasoning_tokens'], ['缓存', 'cached_tokens']] as const).map(([name, key]) => ({ name, type: 'bar', stack: 'tokens', barMaxWidth: 64, data: rows.map(row => row[key]) })),
    }
  }, [data, dimension, token])
  const stats = data ? [
    ['累计 Token 数', data.total_tokens, '所选时间段内全部模型累计使用量'],
    ['日峰值 Token 数', data.peak_tokens, data.peak_tokens_date || '暂无活动'],
    ['总会话次数', data.conversations, '所选时间段内活跃会话（去重）'],
    ['日峰值会话次数', data.peak_conversations, data.peak_conversations_date || '暂无活动'],
  ] as const : []
  return <div className="token-usage-page">
    <header className="token-usage-header"><div><h1>Token 使用分析</h1><p>模型调用、会话与 Token 消耗概览</p></div>
      <DatePicker.RangePicker aria-label="用量分析日期范围" value={range ? [dayjs(range[0]), dayjs(range[1])] : data ? [dayjs(data.start), dayjs(data.end)] : null} allowClear disabledDate={(date, info) => !!info.from && Math.abs(date.startOf('day').diff(info.from.startOf('day'), 'day')) > 365} onChange={dates => setRange(dates?.[0] && dates[1] ? [dates[0].format('YYYY-MM-DD'), dates[1].format('YYYY-MM-DD')] : undefined)} />
    </header>
    {error && <Alert type="error" showIcon title={error} action={<Button onClick={() => setRevision(value => value + 1)}>重试</Button>} />}
    {loading && <div className="token-usage-loading"><Spin /></div>}
    {data && <>
      <Card><div className="token-usage-summary">{stats.map(([title, value, note]) => <div key={title}><Statistic title={title} value={value} formatter={() => <span title={value.toLocaleString()}>{compact(value)}</span>} /><p>{note}</p></div>)}</div></Card>
      <Card title={<div>Token 活动 <small>UTC · 最近一年 {data.activity_start} — {data.activity_end}</small></div>} extra={<Segmented value={mode} options={[{ label: '每日', value: 'daily' }, { label: '每周', value: 'weekly' }, { label: '每月', value: 'monthly' }]} onChange={value => setMode(value as ActivityMode)} />}>
        <div className="token-usage-chart-scroll"><div className="token-usage-activity"><UsageChart option={activity} height={240} label="Token 活动热力图" /></div></div>
      </Card>
      <Card title={<div>Token 构成 <small>全部历史 · 用量最高的 10 项</small></div>} extra={<Segmented value={dimension} options={[{ label: '按模型', value: 'model' }, { label: '按厂商', value: 'provider' }]} onChange={setDimension} />}>
        {(dimension === 'model' ? data.models : data.providers).length ? <div className="token-usage-chart-scroll"><div className="token-usage-composition"><UsageChart option={composition} height={360} label="Token 构成堆叠柱状图" /></div></div> : <Empty description="暂无用量记录" />}
        <p className="token-usage-note">输入含缓存写入，输出不含思考；四类 Token 不重复计数。仅统计成功保存的聊天轮次（含已删除会话），不含标题任务及失败、取消的调用。</p>
      </Card>
    </>}
  </div>
}
