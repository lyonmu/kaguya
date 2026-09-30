package agent

import (
	"context"
	"sync"

	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
)

// TurnEmitter 是单一聊天轮次的发帧入口：串行分配轮内 seq 并按顺序写入
// 无缓冲 dataChan，保证并行工具回调与审批事件不会乱序或并发写同一 channel。
// 它不拥有 channel：Chat 是唯一关闭者，终结前必须等全部 Run 返回。
type TurnEmitter struct {
	ctx context.Context
	out chan<- *dtochat.ChatResp

	mu  sync.Mutex
	seq int64
}

func newTurnEmitter(ctx context.Context, out chan<- *dtochat.ChatResp) *TurnEmitter {
	return &TurnEmitter{ctx: ctx, out: out}
}

// Emit 串行写出一次帧；ctx 取消或对端停止读取后返回 false。
func (e *TurnEmitter) Emit(resp *dtochat.ChatResp) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	resp.Seq = e.seq
	return send(e.ctx, e.out, resp)
}

// Seq 返回已分配的最后一个序号，用于测试观测与排障。
func (e *TurnEmitter) Seq() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.seq
}
