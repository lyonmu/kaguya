package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatblock"
	"github.com/lyonmu/kaguya/internal/global"
)

const (
	// turnFlushInterval 是增量落库的检查周期；未达到阈值时最多每 turnFlushMaxIdle 刷一次。
	turnFlushInterval = time.Second
	// turnFlushMaxIdle 限制已产生内容未落库的最长时间，服务崩溃时最多丢失这段时间的内容。
	turnFlushMaxIdle = 2 * time.Second
	// turnFlushMinBytes 达到后立即刷盘，避免大量正文每次只写极小的增量。
	turnFlushMinBytes = 16 << 10
)

// turnRecorder 把进行中轮次的轨迹按节流批量写入占位行，使取消、断连或进程崩溃后
// 仍能读到已产生的正文与工具记录。成功提交时由事务整体覆盖，不依赖这里的中间状态。
type turnRecorder struct {
	turnID  string
	trace   *turnTrace
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once

	mu         sync.Mutex
	flushed    traceStats     // 最近一次成功提交时的快照统计
	flushedRev map[int64]int64 // sequence 已成功写入的轨迹版本
	lastFlush  time.Time
}

func startTurnRecorder(turnID string, trace *turnTrace) *turnRecorder {
	r := &turnRecorder{turnID: turnID, trace: trace, done: make(chan struct{}), stopped: make(chan struct{}), flushedRev: make(map[int64]int64)}
	go r.loop()
	return r
}

func (r *turnRecorder) loop() {
	defer close(r.stopped)
	ticker := time.NewTicker(turnFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-ticker.C:
			if r.shouldFlush() {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				err := r.flush(ctx)
				cancel()
				if err != nil {
					global.Logger.Sugar().Warnf("flush running turn failed: turn_id=%s err=%v", r.turnID, err)
				}
			}
		}
	}
}

// stop 停止后台刷盘；调用方随后可安全地执行最终 flush 或整轮提交。重复调用安全。
func (r *turnRecorder) stop() {
	r.once.Do(func() {
		close(r.done)
		<-r.stopped
	})
}

func (r *turnRecorder) shouldFlush() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	bytes, revision, toolRev := r.trace.stats()
	if revision == r.flushed.Revision {
		return false
	}
	// 工具调用/结果必须尽快可见；正文按空闲时间或累计字节数节流。
	if toolRev != r.flushed.ToolRevision || bytes-r.flushed.Bytes >= turnFlushMinBytes {
		return true
	}
	return time.Since(r.lastFlush) >= turnFlushMaxIdle
}

// flush 把自上次刷盘以来变化的块写入数据库。整个过程在一个事务内，读者不会看到半个块；
// 只有提交成功后才推进内存水位，失败时下一次 flush 会重试同一批块。
func (r *turnRecorder) flush(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := r.trace.snapshot()
	tx, err := db.EntClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	client := tx.Client()
	// 待推进水位先放局部变量，commit 成功后才写回 recorder。
	flushedRev := make(map[int64]int64, len(snapshot.Blocks))
	for _, item := range snapshot.Blocks {
		b := item.Block
		flushedRev[b.Sequence] = item.Revision
		if item.Revision == 0 || r.flushedRev[b.Sequence] >= item.Revision {
			continue
		}
		// 未结束的块没有真实结束时间，用开始时间占位；end_order 也必须为正。
		finishedAt := b.FinishedAt
		if finishedAt.IsZero() {
			finishedAt = b.StartedAt
		}
		endOrder := b.EndOrder
		if endOrder <= 0 {
			endOrder = b.StartOrder
		}
		var output []byte
		if b.Output != nil {
			output, err = json.Marshal(b.Output)
			if err != nil {
				return err
			}
		}
		if r.flushedRev[b.Sequence] == 0 {
			create := client.KaguyaChatBlock.Create().SetTurnID(r.turnID).SetSequence(b.Sequence).SetType(kaguyachatblock.Type(b.Type)).
				SetText(b.Text).SetToolCallID(b.ToolCallID).SetToolName(b.ToolName).SetInput(b.Input).
				SetProviderExecuted(b.ProviderExecuted).SetIsError(b.IsError).SetErrorMessage(b.ErrorMessage).
				SetStartedAt(b.StartedAt).SetFinishedAt(finishedAt).SetStartOrder(b.StartOrder).SetEndOrder(endOrder)
			if output != nil {
				create.SetOutput(output)
			}
			if _, err := create.Save(ctx); err != nil {
				return err
			}
		} else {
			update := client.KaguyaChatBlock.Update().
				Where(kaguyachatblock.TurnIDEQ(r.turnID), kaguyachatblock.SequenceEQ(b.Sequence)).
				SetText(b.Text).SetToolCallID(b.ToolCallID).SetToolName(b.ToolName).SetInput(b.Input).
				SetProviderExecuted(b.ProviderExecuted).SetIsError(b.IsError).SetErrorMessage(b.ErrorMessage).
				SetStartedAt(b.StartedAt).SetFinishedAt(finishedAt).SetStartOrder(b.StartOrder).SetEndOrder(endOrder).
				ClearOutput()
			if output != nil {
				update.SetOutput(output)
			}
			// 水位只在写入成功后推进，因此这里必须命中已存在且序列唯一的行；
			// 零行更新说明占位块被外部删除，继续推进会永久丢失内容。
			changed, err := update.Save(ctx)
			if err != nil {
				return err
			}
			if changed != 1 {
				return fmt.Errorf("update running turn block: turn_id=%s sequence=%d affected %d rows", r.turnID, b.Sequence, changed)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	r.flushedRev = flushedRev
	r.flushed = traceStats{Bytes: snapshot.Bytes, Revision: snapshot.Revision, ToolRevision: snapshot.ToolRevision}
	r.lastFlush = time.Now()
	return nil
}
