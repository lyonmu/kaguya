import { Tabs } from 'antd'
import type { Conversation, Turn } from '../types'

function executionEvents(turn: Turn) {
  return turn.blocks.flatMap((block, index) => {
    const name = block.tool_name || (block.type === 'reasoning' ? '模型思考' : '模型回复')
    const events = [{ key: `${index}-start`, order: block.start_order ?? index * 2, label: name, time: block.started_at }]
    if (block.end_order) events.push({ key: `${index}-end`, order: block.end_order, label: `${name} · ${block.is_error ? '失败' : '结束'}`, time: block.finished_at })
    return events
  }).sort((a, b) => a.order - b.order)
}

export function Inspector({ conversation, turns, streaming, id }: { conversation?: Conversation; turns: Turn[]; streaming: boolean; id: string }) {
  const last = turns.at(-1)
  const usage = conversation?.usage
  const rows = [
    ['状态', streaming ? '运行中' : last?.status === 'error' ? '生成失败' : last?.status === 'stopped' ? '已停止' : last ? '已完成' : '等待输入'],
    ['模型', last?.model_name || conversation?.model_name || '后端默认模型'],
    ['会话 ID', id || '发送后创建'],
    ['创建时间', conversation ? new Date(conversation.created_at).toLocaleString() : '—'],
    ['已保存轮次', conversation?.turn_count ?? 0],
    ['累计耗时', conversation ? `${(conversation.duration_ms / 1000).toFixed(2)} s` : '—'],
    ['输入 Tokens', usage?.input_tokens ?? '—'],
    ['输出 Tokens', usage?.output_tokens ?? '—'],
    ['缓存 Tokens', usage?.cached_tokens ?? '—'],
    ['思考 Tokens', usage?.reasoning_tokens ?? '—'],
    ['工具调用', conversation?.tool_calls ?? '—'],
  ]
  return <aside className="chat-inspector"><Tabs size="small" items={[
    { key: 'overview', label: 'Overview', children: <><h3>会话与运行信息</h3><div className="chat-info-card">{rows.map(([key, value]) => <div className="chat-kv" key={key}><span>{key}</span><strong>{value}</strong></div>)}</div><p className="chat-muted">累计数据仅包含已成功保存的轮次。</p></> },
    { key: 'trace', label: 'Trace', children: <><h3>执行顺序 · 已加载轮次</h3>{turns.map(turn => <div className="chat-info-card" key={turn.turn_index}><h4>第 {turn.turn_index} 轮</h4><ol className="chat-timeline"><li>用户输入</li>{executionEvents(turn).map(event => <li key={event.key}>{event.label}{event.time && <small>{new Date(event.time).toLocaleTimeString()}</small>}</li>)}<li>{turn.status === 'streaming' ? '生成中' : turn.error ? '本轮未完成' : '完成'}</li></ol></div>)}</> },
    { key: 'debug', label: 'Debug', children: <><h3>最近一轮 · 公开运行信息</h3><pre className="chat-raw">{JSON.stringify(last ? { model_id: last.model_id, api_protocol: last.api_protocol, finish_reason: last.finish_reason, usage: last.usage, status: last.status ?? 'done' } : {}, null, 2)}</pre><p className="chat-muted">不展示服务端私有上下文或配置密钥。</p></> },
  ]} /></aside>
}
