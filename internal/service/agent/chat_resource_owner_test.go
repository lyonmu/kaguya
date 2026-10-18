//go:build darwin || linux

package agent

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/trace"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/lyonmu/kaguya/internal/config"
	"github.com/mattn/go-sqlite3"
)

// 仅诊断模式使用。时间是相对epoch的单调时钟，不记录SQL、参数或请求正文。
// DriverEnd与PoolReleaseUpper夹住连接归还；上界不是精确release事件。
type chatOwnerRecord struct {
	ID                 uint64 `json:"id"`
	Parent             uint64 `json:"parent,omitempty"`
	Connection         uint64 `json:"connection"`
	Owner              string `json:"owner"`
	Operation          string `json:"operation"`
	RequestedNS        int64  `json:"requested_ns"`
	AcquiredNS         int64  `json:"acquired_ns"`
	DriverDoneNS       int64  `json:"driver_done_ns"`
	ReturnedNS         int64  `json:"returned_ns"`
	DriverEndNS        int64  `json:"driver_end_ns"`
	PoolReleaseUpperNS int64  `json:"pool_release_upper_ns"`
	End                string `json:"end"`
	Failed             bool   `json:"failed"`
	NativeCallNS       int64  `json:"native_call_ns"`
	NativeFinishNS     int64  `json:"native_finish_ns"`
	RowsCallsNS        int64  `json:"rows_calls_ns"`
	RowsCalls          int    `json:"rows_calls"`
	TraceStarted       bool   `json:"trace_started,omitempty"`
	TraceEnded         bool   `json:"trace_ended,omitempty"`
}

type chatOwnerProbe struct {
	mu                    sync.Mutex
	epoch                 time.Time
	sequence, connections uint64
	records               []*chatOwnerOperation
}

type chatOwnerOperation struct {
	probe        *chatOwnerProbe
	record       chatOwnerRecord // 仅在probe.mu内读写
	traceCtx     context.Context
	traceTask    *trace.Task
	traceEndOnce sync.Once
}

type chatOwnerContextKey struct{}

func (d *chatResourceDriver) startOwner(ctx context.Context, operation string, parent *chatOwnerOperation) (context.Context, *chatOwnerOperation) {
	if d.owners == nil || !d.capture.Load() {
		return ctx, nil
	}
	return d.owners.start(ctx, operation, parent)
}

func (p *chatOwnerProbe) start(ctx context.Context, operation string, parent *chatOwnerOperation) (context.Context, *chatOwnerOperation) {
	if p == nil {
		return ctx, nil
	}
	owner := "other"
	if parent != nil {
		owner = "transaction-statement"
	} else {
		var pcs [32]uintptr
		n := runtime.Callers(2, pcs[:])
		frames := runtime.CallersFrames(pcs[:n])
		for {
			frame, more := frames.Next()
			for _, entry := range []struct{ suffix, label string }{
				{".(*turnRecorder).flush", "recorder-flush"}, {".saveCompletedTurn", "final-save"},
				{".beginTurn", "begin-turn"}, {".createConversation", "create-conversation"},
				{".loadConversationRow", "load-history"}, {".resolveChatTarget", "chat-config"},
				{".(*TitleQueue).claim", "title-claim"}, {".(*TitleQueue).worker", "title-worker"},
			} {
				if strings.HasSuffix(frame.Function, entry.suffix) {
					owner = entry.label
					break
				}
			}
			if owner != "other" || !more {
				break
			}
		}
	}
	p.mu.Lock()
	if p.epoch.IsZero() {
		p.epoch = time.Now()
	}
	p.sequence++
	op := &chatOwnerOperation{probe: p, record: chatOwnerRecord{ID: p.sequence, Owner: owner, Operation: operation, RequestedNS: time.Since(p.epoch).Nanoseconds()}}
	if parent != nil {
		op.record.Parent = parent.record.ID
	}
	p.records = append(p.records, op)
	p.mu.Unlock()
	// 只有显式owners+trace诊断才走这里。固定标签不含SQL/参数；
	// 每条native操作独立task，通过owner/parent ID关联Ent事务task。
	if trace.IsEnabled() {
		op.traceCtx, op.traceTask = trace.NewTask(ctx, "chat-native-owner")
		op.update(func(r *chatOwnerRecord, _ int64) { r.TraceStarted = true })
		trace.Logf(op.traceCtx, "owner-id", "%d", op.record.ID)
		trace.Log(op.traceCtx, "owner", owner)
		trace.Log(op.traceCtx, "operation", operation)
		if parent != nil {
			trace.Logf(op.traceCtx, "parent-owner-id", "%d", op.record.Parent)
		}
	}
	return context.WithValue(ctx, chatOwnerContextKey{}, op), op
}

func (op *chatOwnerOperation) nativeRegion(name string) *trace.Region {
	if op == nil || op.traceTask == nil {
		return nil
	}
	return trace.StartRegion(op.traceCtx, name)
}

func (op *chatOwnerOperation) endTrace() {
	if op != nil && op.traceTask != nil {
		op.traceEndOnce.Do(func() {
			op.traceTask.End()
			op.update(func(r *chatOwnerRecord, _ int64) { r.TraceEnded = true })
		})
	}
}

func (op *chatOwnerOperation) update(fn func(*chatOwnerRecord, int64)) {
	if op == nil {
		return
	}
	// epoch在operation发布前初始化且不再变化。先取事件时间，避免将
	// probe.mu的诊断记录排队误算成native返回或连接持有时间。
	now := time.Since(op.probe.epoch).Nanoseconds()
	op.probe.mu.Lock()
	defer op.probe.mu.Unlock()
	fn(&op.record, now)
}

func (op *chatOwnerOperation) returned(err error, releases bool) {
	op.update(func(r *chatOwnerRecord, now int64) {
		if r.ReturnedNS == 0 {
			r.ReturnedNS = now
		}
		r.Failed = r.Failed || err != nil
		if releases && r.PoolReleaseUpperNS == 0 {
			r.PoolReleaseUpperNS = now
		}
	})
	// 子Exec/失败Query不归还父事务连接，但本条操作已结束；
	// task生命周期不能与pool ownership的releases标志混为一谈。
	if releases || err != nil || op != nil && op.record.Operation == "exec" {
		op.endTrace()
	}
}

func (op *chatOwnerOperation) releaseUpper() {
	op.update(func(r *chatOwnerRecord, now int64) {
		if r.Parent == 0 && r.PoolReleaseUpperNS == 0 {
			r.PoolReleaseUpperNS = now
		}
	})
}

func (op *chatOwnerOperation) id() uint64 {
	if op == nil {
		return 0
	}
	op.probe.mu.Lock()
	defer op.probe.mu.Unlock()
	return op.record.ID
}

func (p *chatOwnerProbe) snapshot() []chatOwnerRecord {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	rows := make([]chatOwnerRecord, len(p.records))
	for i, op := range p.records {
		rows[i] = op.record
	}
	return rows
}

type chatOwnerConnector struct {
	dsn   string
	probe *chatOwnerProbe
}

func (c chatOwnerConnector) Driver() driver.Driver { return &sqlite3.SQLiteDriver{} }
func (c chatOwnerConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	base, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	c.probe.mu.Lock()
	c.probe.connections++
	id := c.probe.connections
	c.probe.mu.Unlock()
	return &chatOwnerConn{Conn: base, probe: c.probe, id: id}, nil
}

// SQLiteConn当前没有SessionResetter/Validator，不能凭包装器新添这些接口：
// database/sql会因此改变取消后的keepConnOnRollback及连接丢弃行为。
type chatOwnerConn struct {
	driver.Conn
	probe       *chatOwnerProbe
	id          uint64
	transaction *chatOwnerOperation // database/sql的driverConn锁内读写
}

func (c *chatOwnerConn) entered(ctx context.Context) *chatOwnerOperation {
	op, _ := ctx.Value(chatOwnerContextKey{}).(*chatOwnerOperation)
	if op != nil {
		op.update(func(r *chatOwnerRecord, now int64) {
			r.Connection = c.id
			if r.AcquiredNS == 0 {
				r.AcquiredNS = now
			}
			if c.transaction != nil {
				r.Parent = c.transaction.record.ID
			}
		})
		if op.traceTask != nil {
			trace.Logf(op.traceCtx, "connection-id", "%d", c.id)
		}
	}
	return op
}

func (op *chatOwnerOperation) driverDone(err error, terminal bool, elapsed int64) {
	op.update(func(r *chatOwnerRecord, now int64) {
		r.NativeCallNS = elapsed
		if r.DriverDoneNS == 0 {
			r.DriverDoneNS = now
		}
		r.Failed = r.Failed || err != nil
		if terminal && r.DriverEndNS == 0 {
			r.DriverEndNS = now
			r.End = "exec-return"
		}
	})
}

func (c *chatOwnerConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	op := c.entered(ctx)
	region := op.nativeRegion("native-begin")
	started := time.Now()
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	elapsed := time.Since(started).Nanoseconds()
	if region != nil {
		region.End()
	}
	op.driverDone(err, false, elapsed)
	if err != nil {
		return nil, err
	}
	c.transaction = op
	return &chatOwnerNativeTx{Tx: tx, conn: c, op: op}, nil
}

func (c *chatOwnerConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	op := c.entered(ctx)
	region := op.nativeRegion("native-exec")
	started := time.Now()
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	elapsed := time.Since(started).Nanoseconds()
	if region != nil {
		region.End()
	}
	op.driverDone(err, true, elapsed)
	return result, err
}

func (c *chatOwnerConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	op := c.entered(ctx)
	region := op.nativeRegion("native-query")
	started := time.Now()
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	elapsed := time.Since(started).Nanoseconds()
	if region != nil {
		region.End()
	}
	op.driverDone(err, false, elapsed)
	if err != nil {
		return nil, err
	}
	return &chatOwnerNativeRows{Rows: rows, op: op}, nil
}

func (c *chatOwnerConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	// Ent workload当前直接ExecContext/QueryContext；保持原prepare接口，
	// 不把未覆盖的prepare/Stmt路径伪称成已归属。
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}
func (c *chatOwnerConn) Ping(ctx context.Context) error { return c.Conn.(driver.Pinger).Ping(ctx) }

type chatOwnerNativeTx struct {
	driver.Tx
	conn *chatOwnerConn
	op   *chatOwnerOperation
}

func (tx *chatOwnerNativeTx) finish(kind string, call func() error) error {
	region := tx.op.nativeRegion("native-" + kind)
	started := time.Now()
	err := call()
	elapsed := time.Since(started).Nanoseconds()
	if region != nil {
		region.End()
	}
	tx.op.update(func(r *chatOwnerRecord, now int64) {
		r.NativeFinishNS = elapsed
		if r.DriverEndNS == 0 {
			r.DriverEndNS = now
			r.End = kind
		}
		r.Failed = r.Failed || err != nil
	})
	tx.conn.transaction = nil
	tx.op.endTrace()
	return err
}
func (tx *chatOwnerNativeTx) Commit() error   { return tx.finish("commit", tx.Tx.Commit) }
func (tx *chatOwnerNativeTx) Rollback() error { return tx.finish("rollback", tx.Tx.Rollback) }

type chatOwnerNativeRows struct {
	driver.Rows
	op *chatOwnerOperation
}

func (rows *chatOwnerNativeRows) Close() error {
	region := rows.op.nativeRegion("native-rows-close")
	started := time.Now()
	err := rows.Rows.Close()
	elapsed := time.Since(started).Nanoseconds()
	if region != nil {
		region.End()
	}
	rows.op.update(func(r *chatOwnerRecord, now int64) {
		r.RowsCalls++
		r.RowsCallsNS += elapsed
		if r.DriverEndNS == 0 {
			r.DriverEndNS = now
			r.End = "rows-close"
		}
		r.Failed = r.Failed || err != nil
	})
	rows.op.endTrace()
	return err
}
func (rows *chatOwnerNativeRows) Next(dest []driver.Value) error {
	region := rows.op.nativeRegion("native-rows-next")
	started := time.Now()
	err := rows.Rows.Next(dest)
	elapsed := time.Since(started).Nanoseconds()
	if region != nil {
		region.End()
	}
	rows.op.update(func(r *chatOwnerRecord, _ int64) {
		r.RowsCalls++
		r.RowsCallsNS += elapsed
		r.Failed = r.Failed || err != nil && err != io.EOF
	})
	return err
}

// 保留当前SQLiteRows三个实际column metadata接口，避免影响Atlas/schema。
func (rows *chatOwnerNativeRows) ColumnTypeDatabaseTypeName(i int) string {
	return rows.Rows.(driver.RowsColumnTypeDatabaseTypeName).ColumnTypeDatabaseTypeName(i)
}
func (rows *chatOwnerNativeRows) ColumnTypeNullable(i int) (bool, bool) {
	return rows.Rows.(driver.RowsColumnTypeNullable).ColumnTypeNullable(i)
}
func (rows *chatOwnerNativeRows) ColumnTypeScanType(i int) reflect.Type {
	return rows.Rows.(driver.RowsColumnTypeScanType).ColumnTypeScanType(i)
}

func wrapChatOwnerRows(value any, op *chatOwnerOperation, err error) {
	if op == nil || err != nil {
		return
	}
	if rows, ok := value.(*entsql.Rows); ok && rows.ColumnScanner != nil {
		rows.ColumnScanner = &chatOwnerEntRows{ColumnScanner: rows.ColumnScanner, op: op}
	}
}

type chatOwnerEntRows struct {
	entsql.ColumnScanner
	op *chatOwnerOperation
}

func (rows *chatOwnerEntRows) Close() error {
	err := rows.ColumnScanner.Close()
	rows.op.releaseUpper()
	return err
}
func (rows *chatOwnerEntRows) Next() bool {
	ok := rows.ColumnScanner.Next()
	if !ok {
		rows.op.releaseUpper()
	}
	return ok
}
func (rows *chatOwnerEntRows) NextResultSet() bool {
	ok := rows.ColumnScanner.NextResultSet()
	if !ok {
		rows.op.releaseUpper()
	}
	return ok
}

func openChatOwnerProbeTest(t *testing.T) (context.Context, *sql.DB, *chatResourceDriver) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	dir := t.TempDir()
	cfg := config.DatabaseConfig{Path: filepath.Join(dir, "owner.db"), KeyFile: filepath.Join(dir, "key")}
	if err := os.WriteFile(cfg.KeyFile, []byte(strings.Repeat("ab", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	dsn, err := cfg.SQLiteDSN()
	if err != nil {
		t.Fatal(err)
	}
	owners := &chatOwnerProbe{}
	pool := sql.OpenDB(chatOwnerConnector{dsn: dsn, probe: owners})
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	var cipher string
	if err := pool.QueryRowContext(ctx, "PRAGMA cipher_version").Scan(&cipher); err != nil || cipher == "" {
		t.Fatal("owner probe requires native SQLCipher")
	}
	if _, err := pool.ExecContext(ctx, "CREATE TABLE owner_fixture (id INTEGER); INSERT INTO owner_fixture VALUES (1),(2)"); err != nil {
		t.Fatal(err)
	}
	probe := &chatResourceDriver{Driver: entsql.OpenDB(dialect.SQLite, pool), owners: owners}
	probe.capture.Store(true)
	return ctx, pool, probe
}

func TestChatOwnerProbeTracksRowsAndTransactionReleaseBounds(t *testing.T) {
	ctx, pool, probe := openChatOwnerProbeTest(t)
	var rows entsql.Rows
	if err := probe.Query(ctx, "SELECT id FROM owner_fixture", []any{}, &rows); err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("fixture query produced no row")
	}
	ready := make(chan dialect.Tx, 1)
	failed := make(chan error, 1)
	waitCtx, cancelWait := context.WithCancel(ctx)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		tx, err := probe.Tx(waitCtx)
		if err != nil {
			failed <- err
			return
		}
		ready <- tx
	}()
	t.Cleanup(func() {
		cancelWait()
		_ = rows.Close()
		select {
		case <-joined:
		case <-time.After(time.Second):
			t.Error("probe waiter did not join")
		}
		select {
		case tx := <-ready:
			_ = tx.Rollback()
		default:
		}
	})
	for pool.Stats().WaitCount == 0 {
		select {
		case <-ctx.Done():
			t.Fatal("transaction did not wait for the query owner")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	before := probe.owners.snapshot()
	if len(before) != 2 || before[0].PoolReleaseUpperNS != 0 || before[1].AcquiredNS != 0 {
		t.Fatal("query released before Rows.Close or transaction acquired the held connection")
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	var tx dialect.Tx
	select {
	case tx = <-ready:
	case err := <-failed:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer tx.Rollback()
	if err := tx.Query(ctx, "INSERT INTO owner_fixture VALUES (3) RETURNING id", []any{}, &rows); err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil || id != 3 {
			t.Fatalf("RETURNING value=%d error=%v", id, err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal("commit defer rollback unexpectedly re-ran the transaction")
	}
	after := probe.owners.snapshot()
	if len(after) != 3 || pool.Stats().InUse != 0 {
		t.Fatal("instrument lost an operation or retained the connection")
	}
	query, transaction, statement := after[0], after[1], after[2]
	if query.Connection != transaction.Connection || query.End != "rows-close" || query.DriverEndNS < query.ReturnedNS || query.PoolReleaseUpperNS < query.DriverEndNS {
		t.Fatal("query release interval did not include actual Rows.Close")
	}
	if transaction.AcquiredNS < query.DriverEndNS || transaction.End != "commit" || transaction.DriverEndNS < transaction.DriverDoneNS || transaction.PoolReleaseUpperNS < transaction.DriverEndNS {
		t.Fatal("transaction acquisition/commit bounds were inconsistent")
	}
	if statement.Parent != transaction.ID || statement.Connection != transaction.Connection || statement.PoolReleaseUpperNS != 0 || statement.RowsCalls < 2 {
		t.Fatal("transaction rows falsely claimed connection ownership/release")
	}
	for _, row := range after {
		if row.Failed || row.RequestedNS <= 0 || row.AcquiredNS < row.RequestedNS || row.DriverDoneNS < row.AcquiredNS || row.NativeCallNS <= 0 || row.NativeCallNS > row.DriverDoneNS-row.AcquiredNS {
			t.Fatalf("invalid owner timing for %d", row.ID)
		}
	}
	if transaction.NativeFinishNS <= 0 || transaction.NativeFinishNS > transaction.DriverEndNS-transaction.DriverDoneNS {
		t.Fatal("native commit timing included diagnostic bookkeeping")
	}
}

func TestChatOwnerProbeRetainsSQLiteOptionalInterfaces(t *testing.T) {
	ctx, pool, _ := openChatOwnerProbeTest(t)
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Raw(func(value any) error {
		wrapped, ok := value.(*chatOwnerConn)
		if !ok {
			t.Fatal("native connector was not used")
		}
		_, baseReset := wrapped.Conn.(driver.SessionResetter)
		_, wrappedReset := value.(driver.SessionResetter)
		_, baseValid := wrapped.Conn.(driver.Validator)
		_, wrappedValid := value.(driver.Validator)
		if baseReset != wrappedReset || baseValid != wrappedValid {
			t.Fatal("diagnostics changed canceled transaction connection retention")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.QueryContext(ctx, "SELECT CAST(1 AS INTEGER) AS value")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	types, err := rows.ColumnTypes()
	if err != nil || len(types) != 1 || types[0].ScanType() == nil {
		t.Fatal("diagnostics lost native column metadata")
	}
}

func TestChatOwnerProbeCanceledAcquireDoesNotInventOwnership(t *testing.T) {
	ctx, pool, probe := openChatOwnerProbeTest(t)
	var rows entsql.Rows
	if err := probe.Query(ctx, "SELECT id FROM owner_fixture", []any{}, &rows); err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	blocked, cancel := context.WithTimeout(ctx, 5*time.Millisecond)
	defer cancel()
	if _, err := probe.Tx(blocked); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked acquire error=%v", err)
	}
	records := probe.owners.snapshot()
	if len(records) != 2 || !records[1].Failed || records[1].AcquiredNS != 0 || records[1].DriverDoneNS != 0 || pool.Stats().InUse != 1 {
		t.Fatal("canceled pool waiter invented a native connection/BEGIN")
	}
}

func TestChatOwnerProbeAutomaticRollbackDoesNotInventPreciseRelease(t *testing.T) {
	ctx, pool, probe := openChatOwnerProbeTest(t)
	canceled, cancel := context.WithCancel(ctx)
	tx, err := probe.Tx(canceled)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	cancel()
	for pool.Stats().InUse != 0 {
		select {
		case <-ctx.Done():
			t.Fatal("automatic rollback did not release the pool")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if err := tx.Rollback(); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal("automatic rollback did not finish")
	}
	records := probe.owners.snapshot()
	if len(records) != 1 || records[0].End != "rollback" || records[0].DriverEndNS == 0 || records[0].PoolReleaseUpperNS != 0 {
		t.Fatal("ErrTxDone was used as an invented precise release event")
	}
}

func TestChatOwnerProbeTracedStatementsAndQueryFailureFinish(t *testing.T) {
	if trace.IsEnabled() {
		t.Skip("an existing trace owns the global runtime tracer")
	}
	ctx, pool, probe := openChatOwnerProbeTest(t)
	var output bytes.Buffer
	if err := trace.Start(&output); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(trace.Stop)
	tx, err := probe.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := tx.Exec(ctx, "INSERT INTO owner_fixture VALUES (3)", []any{}, nil); err != nil {
		t.Fatal(err)
	}
	var rows entsql.Rows
	if err := tx.Query(ctx, "SELECT nonexistent_column FROM owner_fixture", []any{}, &rows); err == nil {
		t.Fatal("invalid query unexpectedly succeeded")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	records := probe.owners.snapshot()
	if len(records) != 3 || pool.Stats().InUse != 0 {
		t.Fatal("traced statements lost their owner or retained the connection")
	}
	for _, record := range records {
		if !record.TraceStarted || !record.TraceEnded {
			t.Fatalf("operation %s did not finish its native trace task", record.Operation)
		}
		if record.Parent != 0 && record.PoolReleaseUpperNS != 0 {
			t.Fatal("finishing a child trace task falsely released the parent connection")
		}
	}
	if !records[2].Failed || records[0].End != "rollback" {
		t.Fatal("query failure or rollback was not retained in traced diagnostics")
	}
}
