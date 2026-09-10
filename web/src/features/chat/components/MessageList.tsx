import { useLayoutEffect, useRef } from "react";
import { Alert, Empty, Spin } from "antd";
import { VirtualList } from "./VirtualList";
import { Markdown, CopyButton } from "./Markdown";
import { ActivityBlock } from "./ActivityBlock";
import type { Block, Turn } from "../types";
import kaguyaAvatar from "../../../assets/kaguya.png";
import userAvatar from "../../../assets/lyonmu.png";

export function ContentBlock({ block, streaming = false }: { block: Block; streaming?: boolean }) {
 return block.type === 'text' ? <Markdown text={block.text ?? ''} /> : <ActivityBlock block={block} streaming={streaming} />
}

interface Props {
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
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description="从这里开始，告诉我你的想法"
          />
        </div>
      )}
      <VirtualList
        className="chat-messages"
        items={turns}
        itemKey={(turn) => turn.turn_index}
        estimate={360}
        initialEnd={initialEnd}
        followEnd={streaming}
        onBoundary={(direction) => {
          if (!loading && !streaming)
            void onPageChange(page + direction, direction < 0);
        }}
        renderItem={(turn) => (
          <section className="chat-message-wrap">
            <article className="chat-message">
              <div className="chat-avatar">
                <img src={userAvatar} alt="用户头像" />
              </div>
              <div className="chat-message-body">
                <div className="chat-message-name">
                  你 · {new Date(turn.started_at).toLocaleString()}
                </div>
                <div className="chat-user-text">{turn.user_content}</div>
              </div>
            </article>
            <article className="chat-message">
              <div className="chat-avatar ai">
                <img src={kaguyaAvatar} alt="Kaguya 头像" />
              </div>
              <div className="chat-message-body">
                <div className="chat-message-name">
                  Kaguya {turn.model_name && `· ${turn.model_name}`}
                </div>
                {turn.blocks.map((block, index) => (
                  <ContentBlock key={index} block={block} streaming={turn.status === "streaming"} />
                ))}
                {turn.status === "streaming" && (
                  <div className="chat-muted" role="status">
                    <Spin size="small" /> 正在生成…
                  </div>
                )}
                {turn.error && (
                  <Alert
                    type={turn.status === "stopped" ? "warning" : "error"}
                    title={turn.error}
                    showIcon
                  />
                )}
                {turn.finish_reason === 'step_limit' && <div className="chat-paused" role="status"><span>达到本轮步数上限，执行进度已保存。</span>{page === totalPages && turn === turns.at(-1) && onContinue && <button type="button" onClick={onContinue} disabled={streaming}>继续执行 →</button>}</div>}
                {turn.status !== 'streaming' && turn.blocks.some(b => b.type === 'text') && <div className="chat-response-actions"><CopyButton label="复制回答" text={turn.blocks.filter(b => b.type === 'text').map(b => b.text || '').join('\n\n')} /></div>}
                {turn.usage && (
                  <div className="chat-turn-meta">
                    {turn.usage.total_tokens.toLocaleString()} tokens ·{" "}
                    {(turn.duration_ms / 1000).toFixed(1)} s · {turn.tool_calls}{" "}
                    次工具调用
                  </div>
                )}
              </div>
            </article>
          </section>
        )}
      />
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
