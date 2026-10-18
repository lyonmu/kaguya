package agent

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/lyonmu/kaguya/internal/config"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyatitlejob"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	"github.com/lyonmu/kaguya/internal/global"
	initialize "github.com/lyonmu/kaguya/internal/init"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// 复用recorder的真实事务故障驱动，但本fixture固定使用加密磁盘SQLCipher。
// connector避免为每个fixture注册全局驱动名；所有密钥均为隔离假值。
type titleFaultConnector struct {
	driver *faultDriver
	dsn    string
}

func (c titleFaultConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &titleFaultConn{faultConn: &faultConn{conn: conn, owner: c.driver}}, nil
}
func (c titleFaultConnector) Driver() driver.Driver { return c.driver }

// 快照使用真实只读事务；不能让包装器降级为旧Begin而拒绝ReadOnly选项。
type titleFaultConn struct{ *faultConn }

// 保留生产SQLite statement的上下文能力，避免旧faultStmt接口让执行中
// 的取消变成只在database/sql获取连接前有效。
func (c *titleFaultConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if err := c.owner.inject(query); err != nil {
		return nil, err
	}
	preparer, ok := c.conn.(driver.ConnPrepareContext)
	if !ok {
		return nil, errors.New("fixture driver lacks context preparation")
	}
	stmt, err := preparer.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &titleFaultStmt{faultStmt: &faultStmt{stmt: stmt, owner: c.owner}}, nil
}

type titleFaultStmt struct{ *faultStmt }

func (s *titleFaultStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	executor, ok := s.stmt.(driver.StmtExecContext)
	if !ok {
		return nil, errors.New("fixture driver lacks context execution")
	}
	return executor.ExecContext(ctx, args)
}

func (s *titleFaultStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	querier, ok := s.stmt.(driver.StmtQueryContext)
	if !ok {
		return nil, errors.New("fixture driver lacks context queries")
	}
	return querier.QueryContext(ctx, args)
}

func (c *titleFaultConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.conn.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("fixture driver lacks context transactions")
	}
	tx, err := beginner.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &faultTx{tx: tx, owner: c.owner}, nil
}

func setupTitleFault(t *testing.T) (context.Context, *ent.Client, *sql.DB, *faultDriver, *TitleQueue, <-chan dtochat.TitleEvent, *observer.ObservedLogs) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	dir := t.TempDir()
	cfg := config.DatabaseConfig{Path: filepath.Join(dir, "title.db"), KeyFile: filepath.Join(dir, "key")}
	if err := os.WriteFile(cfg.KeyFile, []byte(strings.Repeat("ab", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	dsn, err := cfg.SQLiteDSN()
	if err != nil {
		t.Fatal(err)
	}
	probe, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal("cannot create fixture driver")
	}
	fault := &faultDriver{base: probe.Driver()}
	_ = probe.Close()
	conn := sql.OpenDB(titleFaultConnector{driver: fault, dsn: dsn})
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, conn)))
	t.Cleanup(func() { _ = client.Close() })
	var version string
	if err := conn.QueryRowContext(ctx, "PRAGMA cipher_version").Scan(&version); err != nil || version == "" {
		t.Fatal("title fault fixture requires SQLCipher")
	}
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldID, oldLogger, oldPublisher := db.EntClient, global.Id, global.Logger, DefaultTitlePublisher
	gen := &chatTestID{}
	gen.value.Store(123456789012340)
	core, logs := observer.New(zap.WarnLevel)
	logger := zap.New(core)
	db.EntClient, global.Id, global.Logger = client, gen, logger
	DefaultTitlePublisher = NewTitlePublisher()
	t.Cleanup(func() {
		db.EntClient, global.Id, global.Logger, DefaultTitlePublisher = oldClient, oldID, oldLogger, oldPublisher
	})
	if err := initialize.Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetGlobalAgentsPaths([]string{}).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	subscription, unsubscribe, err := DefaultTitlePublisher.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	queue := NewTitleQueue(ctx, client, logger)
	t.Cleanup(func() {
		queue.Close()
		waitTitleQueueClosed(t, queue)
		unsubscribe()
		DefaultTitlePublisher.Close()
	})
	return ctx, client, conn, fault, queue, subscription.Events, logs
}

func TestTitleJobDriverCommitFailure(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_commit", true: "after_commit"}[committed], func(t *testing.T) {
			ctx, client, _, fault, queue, events, logs := setupTitleFault(t)
			var calls atomic.Int32
			configureTitleModel(t, ctx, client, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"title","object":"chat.completion","model":"queue-model","choices":[{"index":0,"message":{"role":"assistant","content":"事务标题"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
			})
			createTitleConversation(t, ctx, client, "commit-fault")
			if err := queue.Enqueue(ctx, "commit-fault", "question", ""); err != nil {
				t.Fatal(err)
			}
			requireTitleJobEvent(t, events, "commit-fault", kaguyatitlejob.StatusPending, 0)
			job, err := queue.claim()
			if err != nil || job == nil {
				t.Fatalf("claim err=%v", err)
			}
			requireTitleJobEvent(t, events, "commit-fault", kaguyatitlejob.StatusRunning, 1)
			client.KaguyaConversation.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					value, err := next.Mutate(ctx, mutation)
					if _, titleWrite := mutation.(*ent.KaguyaConversationMutation).Title(); titleWrite && err == nil {
						if committed {
							fault.failNextCommitted()
						} else {
							fault.failNext("COMMIT")
						}
					}
					return value, err
				})
			})
			queue.execute(ctx, job)
			stored, err := client.KaguyaTitleJob.Get(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			row, err := client.KaguyaConversation.Get(ctx, job.ConversationID)
			if err != nil {
				t.Fatal(err)
			}
			if committed {
				if stored.Status != kaguyatitlejob.StatusSucceeded || stored.Revision != 2 || row.Title != "事务标题" || row.TitleRevision != 1 {
					t.Fatalf("uncertain commit lost actual state: status=%s job_rev=%d title_rev=%d", stored.Status, stored.Revision, row.TitleRevision)
				}
				requireNoTitleEvent(t, events)
				// 迟到的旧尝试和明确补生成均不得复活已成功任务或重复计量。
				queue.execute(ctx, job)
				if err := queue.Enqueue(ctx, job.ConversationID, "late", ""); err != nil {
					t.Fatal(err)
				}
				requireNoTitleEvent(t, events)
			} else {
				if stored.Status != kaguyatitlejob.StatusFailed || stored.Revision != 2 || row.Title != defaultConversationTitle || row.TitleRevision != 0 {
					t.Fatalf("failed commit did not roll back: status=%s job_rev=%d title_rev=%d", stored.Status, stored.Revision, row.TitleRevision)
				}
				requireTitleJobEvent(t, events, job.ConversationID, kaguyatitlejob.StatusFailed, 2)
				requireNoTitleEvent(t, events)
			}
			response, err := (&AgentSvc{}).ConversationTitleSnapshots(ctx, &dtochat.TitleSnapshotsReq{IDs: []string{job.ConversationID}})
			if err != nil || len(response.Items) != 1 || response.Items[0].Title != row.Title || response.Items[0].TitleRevision != row.TitleRevision || response.Items[0].JobStatus == nil || *response.Items[0].JobStatus != string(stored.Status) || response.Items[0].JobRevision != stored.Revision {
				t.Fatalf("snapshot did not recover committed state: err=%v", err)
			}
			usage, err := client.KaguyaTaskUsage.Query().All(ctx)
			if err != nil || calls.Load() != 1 || len(usage) != 1 || usage[0].TotalTokens != 7 {
				t.Fatalf("duplicated/lost model usage: calls=%d rows=%d err=%v", calls.Load(), len(usage), err)
			}
			if logs.Len() != 1 || logs.All()[0].Message != "save conversation title failed" {
				t.Fatalf("unsafe/unexpected diagnostics=%d", logs.Len())
			}
		})
	}
}

// 独占生产同型的单连接池，让收尾真实等待数据库；不缩短产品3秒预算。
func TestTitleQueueBlockedCleanupExpiresBeforeDone(t *testing.T) {
	for _, reverting := range []bool{false, true} {
		t.Run(map[bool]string{false: "finish", true: "revert"}[reverting], func(t *testing.T) {
			ctx, client, conn, _, queue, events, logs := setupTitleFault(t)
			held := make(chan *sql.Conn, 1)
			cleanupStarted := make(chan struct{}, 1)
			var observedDeadline time.Time
			var deadlineMu sync.Mutex
			var calls atomic.Int32
			configureTitleModel(t, ctx, client, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				http.Error(w, "fake-secret", http.StatusBadRequest)
			})
			client.KaguyaTaskUsage.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					value, err := next.Mutate(ctx, mutation)
					if err != nil {
						return value, err
					}
					reserved, reserveErr := conn.Conn(ctx)
					if reserveErr != nil {
						return nil, reserveErr
					}
					held <- reserved
					if reverting {
						queue.Close()
					}
					return value, nil
				})
			})
			client.KaguyaTitleJob.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					m := mutation.(*ent.KaguyaTitleJobMutation)
					status, present := m.Status()
					if present && (status == kaguyatitlejob.StatusFailed || (status == kaguyatitlejob.StatusPending && m.Op().Is(ent.OpUpdate))) {
						deadline, ok := ctx.Deadline()
						if !ok {
							return nil, errors.New("cleanup lacks bounded deadline")
						}
						deadlineMu.Lock()
						observedDeadline = deadline
						deadlineMu.Unlock()
						cleanupStarted <- struct{}{}
					}
					return next.Mutate(ctx, mutation)
				})
			})
			createTitleConversation(t, ctx, client, "blocked-cleanup")
			if err := queue.Enqueue(ctx, "blocked-cleanup", "question", ""); err != nil {
				t.Fatal(err)
			}
			requireTitleJobEvent(t, events, "blocked-cleanup", kaguyatitlejob.StatusPending, 0)
			queue.Start()
			var reserved *sql.Conn
			select {
			case reserved = <-held:
			case <-ctx.Done():
				t.Fatal("model did not reach blocked database fixture")
			}
			defer reserved.Close()
			select {
			case <-cleanupStarted:
			case <-ctx.Done():
				t.Fatal("cleanup did not start")
			}
			started := time.Now()
			deadlineMu.Lock()
			deadline := observedDeadline
			deadlineMu.Unlock()
			if remaining := time.Until(deadline); remaining < 2500*time.Millisecond || remaining > 3*time.Second {
				t.Fatalf("not the product 3s cleanup deadline: %s", remaining)
			}
			queue.Close()
			select {
			case <-queue.Done():
				t.Fatal("Done closed while cleanup still owns database wait")
			case <-time.After(100 * time.Millisecond):
			}
			waitTitleQueueClosed(t, queue)
			elapsed := time.Since(started)
			if elapsed < 2500*time.Millisecond || elapsed > 5*time.Second {
				t.Fatalf("cleanup duration=%s", elapsed)
			}
			// 消费唯一真实领取事件；持久收尾失败不得发布假failed/pending。
			requireTitleJobEvent(t, events, "blocked-cleanup", kaguyatitlejob.StatusRunning, 1)
			requireNoTitleEvent(t, events)
			if err := reserved.Close(); err != nil {
				t.Fatal(err)
			}
			job, err := client.KaguyaTitleJob.Query().Only(ctx)
			if err != nil || job.Status != kaguyatitlejob.StatusRunning || job.Revision != 1 || calls.Load() != 1 {
				t.Fatalf("blocked cleanup invented terminal state: calls=%d err=%v", calls.Load(), err)
			}
			if stats := conn.Stats(); stats.InUse != 0 || stats.WaitDuration < 2500*time.Millisecond {
				t.Fatalf("database wait/ownership not exercised: %+v", stats)
			}
			for _, entry := range logs.All() {
				if strings.Contains(entry.Message, "fake-secret") || strings.Contains(fmt.Sprint(entry.ContextMap()), "fake-secret") {
					t.Fatal("provider secret leaked")
				}
			}
			if err := ReconcileTitleJobs(ctx); err != nil {
				t.Fatal(err)
			}
			requireTitleJobEvent(t, events, "blocked-cleanup", kaguyatitlejob.StatusPending, 2)
			// Done后的Close幂等；随后关闭数据库，worker不再有任何持久化工作。
			queue.Close()
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTitleQueueCloseCancelsBlockedRequest(t *testing.T) {
	for _, enqueue := range []bool{true, false} {
		t.Run(map[bool]string{true: "enqueue", false: "wait"}[enqueue], func(t *testing.T) {
			ctx, client, conn, _, queue, events, _ := setupTitleFault(t)
			createTitleConversation(t, ctx, client, "blocked-request")
			reserved, err := conn.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer reserved.Close()
			before := conn.Stats().WaitCount
			requestDone := make(chan error, 1)
			go func() {
				if enqueue {
					requestDone <- queue.Enqueue(context.Background(), "blocked-request", "question", "")
				} else {
					requestDone <- queue.Wait(context.Background(), "blocked-request")
				}
			}()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for conn.Stats().WaitCount == before {
				select {
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal("request did not enter database pool wait")
				}
			}
			closeDone := make(chan struct{})
			go func() { queue.Close(); close(closeDone) }()
			var closedPromptly bool
			select {
			case <-closeDone:
				closedPromptly = true
			case <-time.After(time.Second):
			}
			var requestErr error
			var canceledPromptly bool
			select {
			case requestErr = <-requestDone:
				canceledPromptly = true
			case <-time.After(time.Second):
			}
			// 即使回归失败，也解除真实连接阻塞并排空goroutine，不能污染后续测试。
			if err := reserved.Close(); err != nil {
				t.Fatal(err)
			}
			if !closedPromptly {
				<-closeDone
			}
			if !canceledPromptly {
				requestErr = <-requestDone
			}
			waitTitleQueueClosed(t, queue)
			if !closedPromptly || !canceledPromptly || !errors.Is(requestErr, context.Canceled) {
				t.Fatalf("Close did not cancel its blocked request: close=%v request=%v err=%v", closedPromptly, canceledPromptly, requestErr)
			}
			if count, err := client.KaguyaTitleJob.Query().Count(ctx); err != nil || count != 0 {
				t.Fatalf("request persisted after close: count=%d err=%v", count, err)
			}
			requireNoTitleEvent(t, events)
		})
	}
}

// 第二个真实SQLCipher连接持有写锁，主连接已经进入SQL执行而非池等待。
func TestTitleQueueWriteLockCleanup(t *testing.T) {
	for _, reverting := range []bool{false, true} {
		t.Run(map[bool]string{false: "finish", true: "revert"}[reverting], func(t *testing.T) {
			ctx, client, pool, _, queue, events, _ := setupTitleFault(t)
			var sequence int
			var schema, databasePath string
			if err := pool.QueryRowContext(ctx, "PRAGMA database_list").Scan(&sequence, &schema, &databasePath); err != nil {
				t.Fatal(err)
			}
			cfg := config.DatabaseConfig{Path: databasePath, KeyFile: filepath.Join(filepath.Dir(databasePath), "key")}
			dsn, err := cfg.SQLiteDSN()
			if err != nil {
				t.Fatal("cannot resolve isolated writer configuration")
			}
			writerPool, err := sql.Open("sqlite3", dsn)
			if err != nil {
				t.Fatal("cannot open isolated writer")
			}
			defer writerPool.Close()
			writer, err := writerPool.Conn(ctx)
			if err != nil {
				t.Fatal("cannot acquire isolated writer")
			}
			defer writer.Close()
			defer func() { _, _ = writer.ExecContext(context.Background(), "ROLLBACK") }()
			locked := make(chan error, 1)
			cleanupStarted := make(chan time.Time, 1)
			configureTitleModel(t, ctx, client, func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "fake-secret", http.StatusBadRequest)
			})
			client.KaguyaTaskUsage.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					value, err := next.Mutate(ctx, mutation)
					if err != nil {
						return value, err
					}
					_, lockErr := writer.ExecContext(ctx, "BEGIN IMMEDIATE")
					locked <- lockErr
					if reverting {
						queue.Close()
					}
					return value, lockErr
				})
			})
			client.KaguyaTitleJob.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					m := mutation.(*ent.KaguyaTitleJobMutation)
					status, present := m.Status()
					if present && (status == kaguyatitlejob.StatusFailed || status == kaguyatitlejob.StatusPending && m.Op().Is(ent.OpUpdate)) {
						deadline, ok := ctx.Deadline()
						if !ok {
							return nil, errors.New("cleanup lacks bounded deadline")
						}
						cleanupStarted <- deadline
					}
					return next.Mutate(ctx, mutation)
				})
			})
			createTitleConversation(t, ctx, client, "write-lock")
			if err := queue.Enqueue(ctx, "write-lock", "question", ""); err != nil {
				t.Fatal(err)
			}
			requireTitleJobEvent(t, events, "write-lock", kaguyatitlejob.StatusPending, 0)
			queue.Start()
			select {
			case err := <-locked:
				if err != nil {
					t.Fatal("fixture failed to acquire real write lock")
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var deadline time.Time
			select {
			case deadline = <-cleanupStarted:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if remaining := time.Until(deadline); remaining < 2500*time.Millisecond || remaining > 3*time.Second {
				t.Fatalf("not the unchanged product cleanup budget: %s", remaining)
			}
			started := time.Now()
			queue.Close()
			select {
			case <-queue.Done():
				t.Fatal("worker declared Done before actual blocked SQL returned")
			case <-time.After(100 * time.Millisecond):
			}
			var timely bool
			select {
			case <-queue.Done():
				timely = true
			case <-time.After(time.Until(deadline) + time.Second):
			}
			elapsed := time.Since(started)
			// 无论成功/失败都先解除锁，再join worker，保持fixture清理安全。
			if _, err := writer.ExecContext(context.Background(), "ROLLBACK"); err != nil {
				t.Fatal("cannot release isolated write lock")
			}
			waitTitleQueueClosed(t, queue)
			if !timely {
				t.Fatalf("SQL write lock outlived cleanup deadline: elapsed=%s", elapsed)
			}
			requireTitleJobEvent(t, events, "write-lock", kaguyatitlejob.StatusRunning, 1)
			requireNoTitleEvent(t, events)
			job, err := client.KaguyaTitleJob.Query().Only(ctx)
			if err != nil || job.Status != kaguyatitlejob.StatusRunning || job.Revision != 1 {
				t.Fatalf("blocked write invented committed status: err=%v", err)
			}
			if pool.Stats().InUse != 0 {
				t.Fatal("SQL execution retained database ownership after Done")
			}
			t.Logf("SQLCipher real write lock cleanup elapsed=%s", elapsed)
		})
	}
}
