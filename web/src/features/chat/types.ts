import type { MemoryRefs } from '../memory/types'
export interface Usage {
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cached_tokens: number
  cache_creation_tokens: number
  reasoning_tokens: number
}

export interface ConversationContext {
  conversation_id: string
  turn_index: number
  model_id: string
  model_name: string
  context_tokens: number | null
  context_window: number
  effective_window: number
  window_ratio: number
  percent: number | null
  max_window_percent: number | null
}

export interface ToolOutput {
  type: 'text' | 'error' | 'media'
  text?: string
  data?: string
  media_type?: string
  code?: number
}

export interface Block {
  details_deferred?: boolean
  has_output?: boolean
  type: 'text' | 'reasoning' | 'tool_call' | 'tool_result'
  phase?: 'start' | 'delta' | 'block_end'
  text?: string
  tool_call_id?: string
  tool_name?: string
  input?: string
  output?: ToolOutput
  is_error?: boolean
  error_message?: string
  provider_executed?: boolean
  sequence?: number
  started_at?: string
  finished_at?: string
  start_order?: number
  end_order?: number
}

/** 审批状态：pending 等待裁决；approved/consumed 表示授权已消费；其余为终态。 */
export type ApprovalStatus = 'pending' | 'approved' | 'rejected' | 'expired' | 'canceled' | 'consumed'

/** 一次工具审批的完整状态；input 只在当前连接展示，不写入审计。 */
export interface Approval {
  approval_id: string
  tool_call_id: string
  tool_name: string
  tool_identity?: string
  effect: 'read_only' | 'mutating' | 'unknown' | string
  mode: string
  status: ApprovalStatus | string
  input?: string
  expires_at?: number
}

/** 会话权限模式；与后端持久化值共享同一组稳定字符串。 */
export type PermissionMode = 'full_approval' | 'auto_approval' | 'full_access'

/** 裁决接口的幂等结果；重复相同裁决返回同一状态。 */
export interface ApprovalDecision {
  approval_id: string
  status: string
  decision?: string
}

/** 显式引用选择；path 是工作区相对路径，name 仅作展示。 */
export interface Reference {
  type: 'file' | 'directory'
  path: string
  name?: string
}

/** 引用候选：目录与文件统一协议，来自服务端 Stat。 */
export interface ReferenceCandidate {
  type: 'file' | 'directory'
  path: string
  name?: string
}

export interface TitleEvent {
  type: 'conversation.title.updated'
  conversation_id: string
  revision: number
  title?: string
  status?: string
}

export interface ChatFrame {
  finish_reason?: string
  chat: {
    id: string
    flag: 'start' | 'delta' | 'done' | 'error' | 'approval'
    content?: string
    block?: Block
    seq?: number
  }
  approval?: Approval
  model_id: string
  model_name: string
  api_protocol: string
  usage: Usage
  created: number
}

export interface ConversationTitle {
  id: string
  title: string
  revision?: number
  status?: string
}

export interface ConversationTarget {
  id: string
  title: string
  projectId?: string
}

export interface Conversation {
  is_project: boolean
  project_id?: string | null
  id: string
  title: string
  favorite: boolean
  memory_mode?: string
  memory_refs?: MemoryRefs
  turn_count: number
  model_id: string
  model_name: string
  last_model_id?: string
  created_at: string
  last_message_at: string
  duration_ms: number
  tool_calls: number
  usage: Usage
  permission_mode?: string
  title_revision?: number
  title_job_status?: string
  instructions_format?: 'legacy' | 'split'
  instructions_legacy?: boolean
}

export type TurnStatus = 'streaming' | 'done' | 'error' | 'stopped' | 'running' | 'completed' | 'interrupted' | 'canceled' | 'failed'

export interface Turn {
  id?: string
  turn_index: number
  user_content: string
  memory_refs?: MemoryRefs
  model_name: string
  model_id: string
  api_protocol: string
  provider_name?: string
  started_at: string
  finished_at?: string
  duration_ms: number
  tool_calls: number
  finish_reason?: string
  usage?: Usage
  blocks: Block[]
  status?: TurnStatus
  error?: string
}

export interface TurnPage {
  total: number
  page: number
  page_size: number
  total_pages: number
  items: Turn[]
  has_more: boolean
  next_before: number
}

export interface ConversationPage {
  items: Conversation[]
  total: number
  page: number
  page_size: number
}
