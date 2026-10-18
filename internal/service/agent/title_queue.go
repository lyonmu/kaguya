package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	agentruntime "github.com/lyonmu/kaguya/internal/agent/runtime"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatblock"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
	"github.com/lyonmu/kaguya/internal/ent/kaguyatitlejob"
	"github.com/lyonmu/kaguya/internal/global"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
	"go.uber.org/zap"
)

// titleQueueFallbackInterval 是 wake channel 丢失通知后的兜底扫描间隔；
// 数据库积压是权威来源，通知只用于降低延迟。
const titleQueueFallbackInterval = 2 * time.Second

// TitleQueue 是 appRuntime 持有的标题生成 FIFO 队列。每个会话最多一行任务，
// worker 串行消费 pending：排队等待不侵占领取后的 30 秒模型调用预算。
type TitleQueue struct {
	client *ent.Client
	logger *zap.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}

	mu       sync.Mutex
	started  bool
	stopped  bool
	doneOnce sync.Once
	waiters  map[string]map[chan struct{}]struct{}
	attempts map[string]context.CancelFunc
	// cancelRequested 记录改名/删除发生在尝试注册之前的取消，避免抢在
	// attempts 注册前到达的 Cancel 丢失。
	cancelRequested map[string]bool
}

func NewTitleQueue(parent context.Context, client *ent.Client, logger *zap.Logger) *TitleQueue {
	ctx, cancel := context.WithCancel(parent)
	if logger == nil {
		logger = zap.NewNop()
	}
	return &TitleQueue{
		client: client, logger: logger, ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), done: make(chan struct{}),
		waiters: map[string]map[chan struct{}]struct{}{}, attempts: map[string]context.CancelFunc{},
		cancelRequested: map[string]bool{},
	}
}

// Start 启动唯一 worker；重复调用安全。
func (q *TitleQueue) Start() {
	q.mu.Lock()
	if q.started || q.stopped || q.ctx.Err() != nil {
		q.mu.Unlock()
		return
	}
	q.started = true
	q.mu.Unlock()
	go q.worker()
}

// Done 在 worker 完全退出后关闭；运行时的关停必须等待它再关闭数据库。
func (q *TitleQueue) Done() <-chan struct{} { return q.done }

// Notify 非阻塞唤醒 worker；通知丢失由兜底 ticker 补偿。
func (q *TitleQueue) Notify() {
	if q == nil {
		return
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Close 停止接受新任务、取消当前模型调用并唤醒等待者。重复调用安全。
func (q *TitleQueue) Close() {
	if q == nil {
		return
	}
	// 入队持有mutex期间也可能等待数据库。先取消lifetime，让绑定的请求
	// 解除等待并释放mutex，不能先等锁再发出它们退出所需的取消信号。
	q.cancel()
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		return
	}
	q.stopped = true
	for _, cancel := range q.attempts {
		cancel()
	}
	for _, waiters := range q.waiters {
		for waiter := range waiters {
			close(waiter)
		}
	}
	q.waiters = map[string]map[chan struct{}]struct{}{}
	started := q.started
	q.mu.Unlock()
	if !started {
		q.finishLifecycle()
	}
}

func (q *TitleQueue) finishLifecycle() {
	q.doneOnce.Do(func() { close(q.done) })
}

// Cancel 取消指定会话正在执行（或即将开始）的模型调用；用于手动改名或删除。
// 到达早于尝试注册时记录标记，run 在注册后立即检查。
func (q *TitleQueue) Cancel(conversationID string) {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.cancelRequested[conversationID] = true
	cancel := q.attempts[conversationID]
	q.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Enqueue 入队一次标题生成。pending/running 幂等；终态且标题仍为默认时才开启新尝试。
func (q *TitleQueue) Enqueue(ctx context.Context, conversationID, question, answer string) error {
	if q == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	stopRootCancellation := context.AfterFunc(q.ctx, cancel)
	defer func() { stopRootCancellation(); cancel() }()
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped || q.ctx.Err() != nil {
		return ErrServiceStopping
	}
	if err := q.enqueueLocked(ctx, conversationID, question, answer); err != nil {
		return err
	}
	// 显式新尝试清除改名/删除留下的取消标记，避免新任务被旧取消误伤。
	delete(q.cancelRequested, conversationID)
	q.Notify()
	return nil
}

// enqueueLocked 在 queue mutex 内串行化入队：unique(conversation_id) 冲突时重试，
// 保证不会为同一会话插入两行。
func (q *TitleQueue) enqueueLocked(ctx context.Context, conversationID, question, answer string) error {
	question = limitTitleRunes(question, 4000)
	answer = limitTitleRunes(answer, 4000)
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		order, err := global.Id.GenID()
		if err != nil {
			return err
		}
		err = q.upsertJob(ctx, conversationID, question, answer, order)
		if err == nil {
			return nil
		}
		if !ent.IsConstraintError(err) {
			return err
		}
		lastErr = err
	}
	return lastErr
}

func (q *TitleQueue) upsertJob(ctx context.Context, conversationID, question, answer string, order int64) error {
	tx, err := q.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	conversation, err := client.KaguyaConversation.Query().
		Where(kaguyaconversation.IDEQ(conversationID), kaguyaconversation.DeletedAtIsNil()).
		Select(kaguyaconversation.FieldTitle).Only(ctx)
	if ent.IsNotFound(err) || (err == nil && conversation.Title != defaultConversationTitle) {
		return nil
	}
	if err != nil {
		return err
	}
	job, err := client.KaguyaTitleJob.Query().Where(kaguyatitlejob.ConversationIDEQ(conversationID)).Only(ctx)
	var committed *ent.KaguyaTitleJob
	switch {
	case ent.IsNotFound(err):
		committed, err = client.KaguyaTitleJob.Create().
			SetConversationID(conversationID).SetQuestion(question).SetAnswer(answer).SetEnqueueOrder(order).
			SetStatus(kaguyatitlejob.StatusPending).Save(ctx)
	case err != nil:
		return err
	case job.Status == kaguyatitlejob.StatusPending || job.Status == kaguyatitlejob.StatusRunning:
		// 已有在途任务：幂等，不替换来源。
		return nil
	default:
		changed, updateErr := titleJobCAS(client, job).
			SetStatus(kaguyatitlejob.StatusPending).SetQuestion(question).SetAnswer(answer).SetEnqueueOrder(order).
			ClearStartedAt().ClearFinishedAt().SetErrorCode("").Save(ctx)
		err = updateErr
		if changed == 1 {
			committed = transitionedTitleJob(job, kaguyatitlejob.StatusPending)
		}
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	publishTitleJob(committed)
	return nil
}

// titleJobCAS 将任务身份、状态与版本绑定到本次读取；幂等及迟到收尾不增版。
func titleJobCAS(client *ent.Client, job *ent.KaguyaTitleJob) *ent.KaguyaTitleJobUpdate {
	return client.KaguyaTitleJob.Update().Where(kaguyatitlejob.IDEQ(job.ID),
		kaguyatitlejob.StatusEQ(job.Status), kaguyatitlejob.RevisionEQ(job.Revision)).AddRevision(1)
}

func transitionedTitleJob(job *ent.KaguyaTitleJob, status kaguyatitlejob.Status) *ent.KaguyaTitleJob {
	copy := *job
	copy.Status, copy.Revision = status, job.Revision+1
	return &copy
}

// publishTitleJob 只接受调用方已成功提交的值，不再查询可能已转换的数据库行。
func publishTitleJob(job *ent.KaguyaTitleJob) {
	if job == nil {
		return
	}
	revision := job.Revision
	DefaultTitlePublisher.Publish(dtochat.TitleEvent{
		Type: dtochat.TitleJobEventUpdated, ConversationID: job.ConversationID,
		JobStatus: string(job.Status), JobRevision: &revision,
	})
}

// Wait 等待指定会话的任务离开 pending/running，或超时/ctx 取消。
// 没有在途任务时立即返回；waiter 退出即注销，不永久保留 channel。
func (q *TitleQueue) Wait(ctx context.Context, conversationID string) error {
	if q == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	stopRootCancellation := context.AfterFunc(q.ctx, cancel)
	defer func() { stopRootCancellation(); cancel() }()
	status, err := q.jobStatus(ctx, conversationID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil
		}
		return err
	}
	if status != kaguyatitlejob.StatusPending && status != kaguyatitlejob.StatusRunning {
		return nil
	}
	waiter := make(chan struct{})
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		return nil
	}
	if waiters := q.waiters[conversationID]; waiters != nil {
		waiters[waiter] = struct{}{}
	} else {
		q.waiters[conversationID] = map[chan struct{}]struct{}{waiter: {}}
	}
	q.mu.Unlock()
	// 注册后复查：任务可能在查询与注册之间结束。唤醒全部等待者避免永久等待，
	// 虚假唤醒只让 Wait 多读一次状态，不会丢通知。
	if status, statusErr := q.jobStatus(ctx, conversationID); statusErr != nil ||
		(status != kaguyatitlejob.StatusPending && status != kaguyatitlejob.StatusRunning) {
		q.notifyWaiters(conversationID)
	}
	defer func() {
		q.mu.Lock()
		if waiters := q.waiters[conversationID]; waiters != nil {
			if _, ok := waiters[waiter]; ok {
				delete(waiters, waiter)
				if len(waiters) == 0 {
					delete(q.waiters, conversationID)
				}
			}
		}
		q.mu.Unlock()
	}()

	timer := time.NewTimer(conversationTitleTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	case <-waiter:
		return nil
	}
}

func (q *TitleQueue) jobStatus(ctx context.Context, conversationID string) (kaguyatitlejob.Status, error) {
	job, err := q.client.KaguyaTitleJob.Query().
		Where(kaguyatitlejob.ConversationIDEQ(conversationID)).
		Select(kaguyatitlejob.FieldStatus).Only(ctx)
	if err != nil {
		return "", err
	}
	return job.Status, nil
}

// notifyWaiters 在同一把锁内关闭并移除等待者，保证每个 channel 只关一次。
func (q *TitleQueue) notifyWaiters(conversationID string) {
	q.mu.Lock()
	waiters := q.waiters[conversationID]
	delete(q.waiters, conversationID)
	q.mu.Unlock()
	for waiter := range waiters {
		close(waiter)
	}
}

// ReconcileTitleJobs 在启动时把遗留 running 还原为 pending：单实例部署下没有
// 其它进程能继续它们，pending 保留到下一次启动或显式重试。
func ReconcileTitleJobs(ctx context.Context) error {
	if db.EntClient == nil {
		return nil
	}
	for {
		jobs, err := db.EntClient.KaguyaTitleJob.Query().Where(kaguyatitlejob.StatusEQ(kaguyatitlejob.StatusRunning)).Limit(128).All(ctx)
		if err != nil || len(jobs) == 0 {
			return err
		}
		for _, job := range jobs {
			changed, err := titleJobCAS(db.EntClient, job).SetStatus(kaguyatitlejob.StatusPending).
				ClearStartedAt().ClearFinishedAt().SetErrorCode("").Save(ctx)
			if err != nil {
				return err
			}
			if changed == 1 {
				publishTitleJob(transitionedTitleJob(job, kaguyatitlejob.StatusPending))
			}
		}
	}
}

func (q *TitleQueue) worker() {
	defer q.finishLifecycle()
	ticker := time.NewTicker(titleQueueFallbackInterval)
	defer ticker.Stop()
	for {
		q.drain()
		select {
		case <-q.ctx.Done():
			return
		case <-q.wake:
		case <-ticker.C:
		}
	}
}

// drain 顺序领取 pending 任务，直到队列为空或进程退出。
func (q *TitleQueue) drain() {
	for q.ctx.Err() == nil {
		job, err := q.claim()
		if err != nil {
			q.logger.Sugar().Warnf("claim title job failed: %v", err)
			return
		}
		if job == nil {
			return
		}
		q.run(job)
	}
}

// claim 以 CAS pending→running 领取最早入队的任务；竞争失败返回 nil。
func (q *TitleQueue) claim() (*ent.KaguyaTitleJob, error) {
	job, err := q.client.KaguyaTitleJob.Query().
		Where(kaguyatitlejob.StatusEQ(kaguyatitlejob.StatusPending)).
		Order(ent.Asc(kaguyatitlejob.FieldEnqueueOrder)).First(q.ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	changed, err := titleJobCAS(q.client, job).
		SetStatus(kaguyatitlejob.StatusRunning).SetStartedAt(time.Now()).AddAttemptCount(1).
		Save(q.ctx)
	if err != nil {
		return nil, err
	}
	if changed == 0 {
		return nil, nil
	}
	claimed := transitionedTitleJob(job, kaguyatitlejob.StatusRunning)
	claimed.AttemptCount++
	publishTitleJob(claimed)
	return claimed, nil
}

// run 执行单个任务：使用 root context 的派生超时（等待时间不计入 30 秒预算），
// 关停时把 running 还原为 pending。activeWork 只登记真正执行的标题任务。
func (q *TitleQueue) run(job *ent.KaguyaTitleJob) {
	finishWork, ok := startWork()
	if !ok {
		q.revertToPending(context.Background(), job)
		return
	}
	defer finishWork()

	attemptCtx, cancel := context.WithTimeout(q.ctx, conversationTitleTimeout)
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		cancel()
		q.revertToPending(context.Background(), job)
		return
	}
	if q.cancelRequested[job.ConversationID] {
		// 改名/删除先于尝试注册到达：不调用模型，直接终结任务。
		delete(q.cancelRequested, job.ConversationID)
		q.mu.Unlock()
		cancel()
		q.finish(context.WithoutCancel(attemptCtx), job, kaguyatitlejob.StatusCanceled, "title_changed")
		q.notifyWaiters(job.ConversationID)
		return
	}
	q.attempts[job.ConversationID] = cancel
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		delete(q.attempts, job.ConversationID)
		delete(q.cancelRequested, job.ConversationID)
		q.mu.Unlock()
		cancel()
	}()

	q.execute(attemptCtx, job)
	q.notifyWaiters(job.ConversationID)
}

// revertToPending 用独立的有界上下文还原任务状态；关停期间不能依赖 root ctx。
func (q *TitleQueue) revertToPending(ctx context.Context, job *ent.KaguyaTitleJob) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	changed, err := titleJobCAS(q.client, job).SetStatus(kaguyatitlejob.StatusPending).
		ClearStartedAt().ClearFinishedAt().SetErrorCode("").Save(cleanupCtx)
	if err != nil {
		q.logger.Warn("restore title job to pending failed", zap.String("job_id", job.ID), zap.String("reason", "persist_failed"))
	} else if changed == 1 {
		publishTitleJob(transitionedTitleJob(job, kaguyatitlejob.StatusPending))
	}
}

func (q *TitleQueue) execute(ctx context.Context, job *ent.KaguyaTitleJob) {
	if q.ctx.Err() != nil {
		q.revertToPending(ctx, job)
		return
	}
	row, err := q.client.KaguyaConversation.Get(ctx, job.ConversationID)
	if q.ctx.Err() != nil {
		q.revertToPending(ctx, job)
		return
	}
	if err != nil {
		q.finish(ctx, job, kaguyatitlejob.StatusCanceled, "conversation_unavailable")
		return
	}
	// 领取后重新检查：会话已删除或标题已非默认（手动改名）时跳过生成。
	if row.DeletedAt != nil || row.Title != defaultConversationTitle {
		q.finish(ctx, job, kaguyatitlejob.StatusCanceled, "title_changed")
		return
	}
	cfg, err := resolveTitleConfig(ctx, q.client, job.ConversationID)
	if q.ctx.Err() != nil {
		q.revertToPending(ctx, job)
		return
	}
	switch {
	case errors.Is(err, ErrTaskModelNotConfigured):
		q.finish(ctx, job, kaguyatitlejob.StatusFailed, "task_model_not_configured")
		return
	case errors.Is(err, ErrProviderSecretUnavailable):
		q.finish(ctx, job, kaguyatitlejob.StatusFailed, "provider_secret_unavailable")
		return
	case err != nil:
		q.finish(ctx, job, kaguyatitlejob.StatusFailed, "config_unavailable")
		return
	}
	title, err := generateConversationTitle(ctx, cfg, job.Question, job.Answer,
		agentruntime.WithRecorder(servicesystem.NewTaskUsageRecorder(q.client, "title", cfg)))
	if err != nil {
		switch {
		case q.ctx.Err() != nil:
			q.revertToPending(ctx, job)
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			q.finish(context.WithoutCancel(ctx), job, kaguyatitlejob.StatusFailed, "timeout")
		default:
			q.finish(context.WithoutCancel(ctx), job, kaguyatitlejob.StatusFailed, "generation_failed")
			q.logger.Warn("generate conversation title failed", zap.String("conversation_id", job.ConversationID), zap.String("reason", "generation_failed"))
		}
		return
	}
	if q.ctx.Err() != nil {
		q.revertToPending(ctx, job)
		return
	}
	revision, updated, err := q.commitSuccess(context.WithoutCancel(ctx), job, title)
	if err != nil {
		q.finish(context.WithoutCancel(ctx), job, kaguyatitlejob.StatusFailed, "save_failed")
		q.logger.Warn("save conversation title failed", zap.String("conversation_id", job.ConversationID), zap.String("reason", "persist_failed"))
		return
	}
	if !updated {
		q.finish(context.WithoutCancel(ctx), job, kaguyatitlejob.StatusCanceled, "title_changed")
		return
	}
	// 提交成功后才通知前端：事件携带已提交 revision，防止延迟覆盖手动改名。
	DefaultTitlePublisher.Publish(dtochat.TitleEvent{
		Type:           dtochat.TitleEventUpdated,
		ConversationID: job.ConversationID,
		Revision:       revision,
		Title:          title,
	})
}

// commitSuccess 在同一个短事务内条件写入标题并终结任务；不修改最近对话时间/用量。
func (q *TitleQueue) commitSuccess(ctx context.Context, job *ent.KaguyaTitleJob, title string) (int64, bool, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	tx, err := q.client.Tx(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	row, err := client.KaguyaConversation.Query().
		Where(kaguyaconversation.IDEQ(job.ConversationID)).
		Select(kaguyaconversation.FieldTitle, kaguyaconversation.FieldTitleRevision, kaguyaconversation.FieldDeletedAt).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if row.DeletedAt != nil || row.Title != defaultConversationTitle {
		return 0, false, nil
	}
	changed, err := titleJobCAS(client, job).SetStatus(kaguyatitlejob.StatusSucceeded).
		SetFinishedAt(time.Now()).SetErrorCode("").Save(ctx)
	if err != nil || changed != 1 {
		return 0, false, err
	}
	revision := row.TitleRevision + 1
	changed, err = client.KaguyaConversation.Update().
		Where(kaguyaconversation.IDEQ(job.ConversationID), kaguyaconversation.DeletedAtIsNil(),
			kaguyaconversation.TitleEQ(defaultConversationTitle), kaguyaconversation.TitleRevisionEQ(row.TitleRevision)).
		SetTitle(title).SetTitleRevision(revision).Save(ctx)
	if err != nil {
		return 0, false, err
	}
	if changed != 1 {
		return 0, false, nil
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	publishTitleJob(transitionedTitleJob(job, kaguyatitlejob.StatusSucceeded))
	return revision, true, nil
}

// finish 终结任务但保留当前标题；失败不阻塞后续任务。
func (q *TitleQueue) finish(ctx context.Context, job *ent.KaguyaTitleJob, status kaguyatitlejob.Status, code string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	changed, err := titleJobCAS(q.client, job).SetStatus(status).SetFinishedAt(time.Now()).SetErrorCode(code).Save(cleanupCtx)
	if err != nil {
		q.logger.Warn("finish title job failed", zap.String("job_id", job.ID), zap.String("reason", "persist_failed"))
	} else if changed == 1 {
		publishTitleJob(transitionedTitleJob(job, status))
	}
}

// CancelJobs 在会话改名/删除的同一事务内取消在途任务，避免被复活或覆盖。
func CancelJobs(ctx context.Context, client *ent.Client, conversationID, reason string) (*ent.KaguyaTitleJob, error) {
	job, err := client.KaguyaTitleJob.Query().Where(kaguyatitlejob.ConversationIDEQ(conversationID),
		kaguyatitlejob.StatusIn(kaguyatitlejob.StatusPending, kaguyatitlejob.StatusRunning)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	changed, err := titleJobCAS(client, job).SetStatus(kaguyatitlejob.StatusCanceled).
		SetFinishedAt(time.Now()).SetErrorCode(reason).Save(ctx)
	if err != nil || changed != 1 {
		return nil, err
	}
	return transitionedTitleJob(job, kaguyatitlejob.StatusCanceled), nil
}

// EnqueueConversationTitle 为已有首个成功轮次的会话显式补生成或重试标题。
// 已有 pending/running 任务时不替换来源；没有任务时从最早成功轮次恢复问答。
func EnqueueConversationTitle(ctx context.Context, conversationID string) error {
	queue := DefaultTitleQueue
	if queue == nil {
		return nil
	}
	status, err := queue.jobStatus(ctx, conversationID)
	if err == nil && (status == kaguyatitlejob.StatusPending || status == kaguyatitlejob.StatusRunning) {
		return queue.Enqueue(ctx, conversationID, "", "")
	}
	if err != nil && !ent.IsNotFound(err) {
		return err
	}
	turn, err := db.EntClient.KaguyaChatTurn.Query().Where(
		kaguyachatturn.ConversationIDEQ(conversationID),
		kaguyachatturn.StatusEQ(kaguyachatturn.StatusCompleted),
	).Select(kaguyachatturn.FieldUserContent).
		WithBlocks(func(q *ent.KaguyaChatBlockQuery) {
			q.Where(kaguyachatblock.TypeEQ(kaguyachatblock.TypeText)).Order(kaguyachatblock.BySequence())
		}).Order(kaguyachatturn.ByTurnIndex()).First(ctx)
	if ent.IsNotFound(err) {
		// 没有完成轮次：不创建任务，等待 done 后再次请求。
		return nil
	}
	if err != nil {
		return err
	}
	var answer strings.Builder
	for _, block := range turn.Edges.Blocks {
		answer.WriteString(block.Text)
	}
	return queue.Enqueue(ctx, conversationID, turn.UserContent, answer.String())
}

// DefaultTitleQueue 是进程内默认队列，由 appRuntime 在启动时创建并启动。
var DefaultTitleQueue *TitleQueue
