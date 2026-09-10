export interface Usage {
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cached_tokens: number
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

export interface ChatFrame {
  finish_reason?: string
  chat: {
    id: string
    flag: 'start' | 'delta' | 'done' | 'error'
    content?: string
    block?: Block
  }
  model_id: string
  model_name: string
  api_protocol: string
  usage: Usage
  created: number
}

export interface ConversationTitle {
  id: string
  title: string
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
  turn_count: number
  model_id: string
  model_name: string
  created_at: string
  last_message_at: string
  duration_ms: number
  tool_calls: number
  usage: Usage
}

export interface Turn {
  turn_index: number
  user_content: string
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
  status?: 'streaming' | 'done' | 'error' | 'stopped'
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
