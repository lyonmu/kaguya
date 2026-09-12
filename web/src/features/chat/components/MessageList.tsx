import { createContext, useContext, useLayoutEffect, useRef } from "react";
import { Alert, Spin } from "antd";
import { MessagePrimitive, ThreadPrimitive, useAuiState } from "@assistant-ui/react";
import { Markdown, CopyButton } from "./Markdown";
import { ActivityBlock } from "./ActivityBlock";
import { isCanceledStatus, isCompleteStatus, isFailedStatus, isInterruptedStatus, isRunningStatus } from "../status";
import type { Block, Turn } from "../types";
import kaguyaAvatar from "../../../assets/kaguya.png";
import userAvatar from "../../../assets/lyonmu.png";

export function ContentBlock({ block, streaming = false, conversationId, turnIndex }: { block: Block; streaming?: boolean; conversationId?: string; turnIndex?: number }) {
 return block.type === 'text' ? <Markdown text={block.text ?? ''} streaming={streaming && block.phase !== 'block_end'} /> : <ActivityBlock block={block} streaming={streaming} conversationId={conversationId} turnIndex={turnIndex} />
}

interface Props {
  conversationId?: string;
  turns: Turn[];
  loading: boolean;
  streaming: boolean;
  page: number;
  totalPages: number;
  initialEnd: boolean;
  onContinue?: () => void;
  onPageChange: (page: number, fromEnd?: boolean) => Promise<void>;
}

export function MessageList({
  conversationId,
  turns,
  loading,
  streaming,
  page,
  totalPages,
  initialEnd,
  onPageChange,
  onContinue,
}: Props) {
  const rail = useRef<HTMLElement>(null);
  const viewport = useRef<HTMLDivElement>(null)
  const boundaryTime = useRef(0)
  const touchY = useRef(0)
  const boundary = (direction: -1 | 1) => {
    const element = viewport.current
    if (!element || loading || streaming || Date.now() - boundaryTime.current < 500) return
    if ((direction < 0 && element.scrollTop <= 0) || (direction > 0 && element.scrollTop + element.clientHeight >= element.scrollHeight - 1)) {
      boundaryTime.current = Date.now()
      void onPageChange(page + direction, direction < 0)
    }
  }
  useLayoutEffect(() => {
    const selected = rail.current?.querySelector<HTMLElement>(
      '[aria-current="page"]',
    );
    if (selected && rail.current)
      rail.current.scrollTop =
        selected.offsetTop - rail.current.clientHeight / 2;
  }, [page, totalPages]);
  return (
    <div className="chat-message-panel">
      {loading && (
        <div className="chat-history-loading" role="status">
          <Spin size="small" /> 加载对话…
        </div>
      )}
      {!loading && !turns.length && (
        <div className="chat-welcome">
          <div className="chat-welcome-logo">
            <img src={kaguyaAvatar} alt="Kaguya" />
          </div>
          <h1>今天有什么需要我帮忙的吗？</h1>
          <p>告诉 Kaguya 你在想什么，我会和你一起找到答案。</p>

        </div>
      )}
      <ContinueContext.Provider value={{ onContinue: page === totalPages ? onContinue : undefined, streaming, conversationId }}>
        <ThreadPrimitive.Viewport ref={viewport} key={`${page}:${initialEnd}`} className="chat-messages" autoScroll={initialEnd} tabIndex={0}
          onWheel={event => { if (event.deltaY) boundary(event.deltaY < 0 ? -1 : 1) }}
          onTouchStart={event => { touchY.current = event.touches[0].clientY }}
          onTouchEnd={event => { const delta = touchY.current - event.changedTouches[0].clientY; if (Math.abs(delta) > 30) boundary(delta < 0 ? -1 : 1) }}
          onKeyDown={event => {
            if (event.target !== event.currentTarget) return
            if (event.key === 'PageUp' || event.key === 'ArrowUp') boundary(-1)
            if (event.key === 'PageDown' || event.key === 'ArrowDown') boundary(1)
          }}>
          <ThreadPrimitive.Messages components={{ UserMessage: RuntimeMessage, AssistantMessage: RuntimeMessage }} />
          <ThreadPrimitive.ScrollToBottom className="chat-scroll-bottom" aria-label="滚动到最新消息">↓ 最新消息</ThreadPrimitive.ScrollToBottom>
        </ThreadPrimitive.Viewport>
      </ContinueContext.Provider>
      {totalPages > 0 && (
        <nav ref={rail} className="chat-page-rail" aria-label="对话内容分页">
          {Array.from({ length: totalPages }, (_, index) => index + 1).map(
            (value) => (
              <button
                key={value}
                aria-label={`第 ${value} 页`}
                title={`第 ${value} / ${totalPages} 页`}
                aria-current={page === value ? "page" : undefined}
                disabled={loading || streaming}
                onClick={() => void onPageChange(value)}
              >
                <span />
              </button>
            ),
          )}
        </nav>
      )}
    </div>
  );
}

const ContinueContext = createContext<{ onContinue?: () => void; streaming: boolean; conversationId?: string }>({ streaming: false })
function RuntimeMessage() {
  const turn = useAuiState(state => state.message.metadata.custom.turn) as Turn
  const role = useAuiState(state => state.message.role)
  const isLast = useAuiState(state => state.message.isLast)
  const context = useContext(ContinueContext)
  const onContinue = isLast ? context.onContinue : undefined
  const streaming = context.streaming
  const running = isRunningStatus(turn.status)
  const interrupted = isInterruptedStatus(turn.status)
  const canceled = isCanceledStatus(turn.status)
  const failed = isFailedStatus(turn.status)
  const incompleteMessage = turn.error || (interrupted ? '本轮生成已中断，已保留已产生的内容。' : canceled ? '本轮已取消，已保留已产生的内容。' : failed ? '本轮生成失败，已保留已产生的内容。' : '')
  return (
          <MessagePrimitive.Root className="chat-message-wrap" data-role={role}>
            {role === "user" && <article className="chat-message">
              <div className="chat-avatar">
                <img src={userAvatar} alt="用户头像" />
              </div>
              <div className="chat-message-body">
                <div className="chat-message-name">
                  你 · {new Date(turn.started_at).toLocaleString()}
                </div>
                <div className="chat-user-text">{turn.user_content}</div>
                <div className="chat-response-actions"><CopyButton label="复制提问" text={turn.user_content} /></div>
              </div>
            </article>}
            {role === "assistant" && <article className="chat-message">
              <div className="chat-avatar ai">
                <img src={kaguyaAvatar} alt="Kaguya 头像" />
              </div>
              <div className="chat-message-body">
                <div className="chat-message-name">
                  Kaguya {turn.model_name && `· ${turn.model_name}`}
                </div>
                {turn.blocks.map((block, index) => (
                  <ContentBlock key={block.sequence ?? index} block={block} streaming={running} conversationId={context.conversationId} turnIndex={turn.turn_index} />
                ))}
                {running && (
                  <div className="chat-muted" role="status">
                    <Spin size="small" /> 正在生成…
                  </div>
                )}
                {incompleteMessage && (
                  <Alert
                    type={interrupted || canceled ? "warning" : "error"}
                    title={incompleteMessage}
                    showIcon
                  />
                )}
                {turn.finish_reason === 'step_limit' && <div className="chat-paused" role="status"><span>达到本轮步数上限，执行进度已保存。</span>{onContinue && <button type="button" onClick={onContinue} disabled={streaming}>继续执行 →</button>}</div>}
                {!running && turn.blocks.some(b => b.type === 'text') && <div className="chat-response-actions"><CopyButton label="复制回答" text={turn.blocks.filter(b => b.type === 'text').map(b => b.text || '').join('\n\n')} /></div>}
                {isCompleteStatus(turn.status) && turn.usage && (
                  <div className="chat-turn-meta">
                    {turn.usage.total_tokens.toLocaleString()} tokens ·{" "}
                    {(turn.duration_ms / 1000).toFixed(1)} s · {turn.tool_calls}{" "}
                    次工具调用
                  </div>
                )}
              </div>
            </article>}
          </MessagePrimitive.Root>
  )
}
