package agent

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatblock"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	"github.com/lyonmu/kaguya/internal/global"
	initialize "github.com/lyonmu/kaguya/internal/init"
	"go.uber.org/zap"
)

// errInjectedFailure 模拟数据库写入失败，用于验证增量落库的水位推进时机。
var errInjectedFailure = errors.New("injected database failure")

// faultDriver 把连接交给真实 SQLite 驱动，并按语句内容注入一次失败。
// 只用于测试：占位块写入失败或 commit 失败后，recorder 必须保持旧水位并重试。
type faultDriver struct {
	base    driver.Driver
	mu      sync.Mutex
	failOn  []string
	failNth map[string]int // 语句模式的第几次匹配触发注入
	seen    map[string]int
}

func (d *faultDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &faultConn{conn: conn, owner: d}, nil
}

// failNext 让下一次包含 substr 的语句返回注入错误，作用一次。
func (d *faultDriver) failNext(substr string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failNth == nil {
		d.failNth, d.seen = make(map[string]int), make(map[string]int)
	}
	d.failNth[substr] = d.seen[substr] + 1
	d.failOn = append(d.failOn, substr)
}

// failOnNth 让包含 substr 的第 n 次匹配返回注入错误，用于制造部分写入失败。
func (d *faultDriver) failOnNth(substr string, n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failNth == nil {
		d.failNth, d.seen = make(map[string]int), make(map[string]int)
	}
	d.failNth[substr] = n
	d.failOn = append(d.failOn, substr)
}

func (d *faultDriver) inject(query string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, substr := range d.failOn {
		if !strings.Contains(query, substr) {
			continue
		}
		d.seen[substr]++
		if d.seen[substr] != d.failNth[substr] {
			continue
		}
		d.failOn = append(d.failOn[:i], d.failOn[i+1:]...)
		return errInjectedFailure
	}
	return nil
}

type faultConn struct {
	conn  driver.Conn
	owner *faultDriver
}

func (c *faultConn) Prepare(query string) (driver.Stmt, error) {
	if err := c.owner.inject(query); err != nil {
		return nil, err
	}
	stmt, err := c.conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &faultStmt{stmt: stmt, owner: c.owner}, nil
}

func (c *faultConn) Close() error { return c.conn.Close() }

func (c *faultConn) Begin() (driver.Tx, error) { //nolint:staticcheck // 底层驱动只实现旧接口
	tx, err := c.conn.Begin() //nolint:staticcheck // 底层驱动只实现旧接口
	if err != nil {
		return nil, err
	}
	return &faultTx{tx: tx, owner: c.owner}, nil
}

type faultStmt struct {
	stmt  driver.Stmt
	owner *faultDriver
}

func (s *faultStmt) Close() error  { return s.stmt.Close() }
func (s *faultStmt) NumInput() int { return s.stmt.NumInput() }

func (s *faultStmt) Exec(args []driver.Value) (driver.Result, error) { //nolint:staticcheck // 底层驱动只实现旧接口
	return s.stmt.Exec(args) //nolint:staticcheck // 底层驱动只实现旧接口
}

func (s *faultStmt) Query(args []driver.Value) (driver.Rows, error) { //nolint:staticcheck // 底层驱动只实现旧接口
	return s.stmt.Query(args) //nolint:staticcheck // 底层驱动只实现旧接口
}

type faultTx struct {
	tx    driver.Tx
	owner *faultDriver
}

func (t *faultTx) Commit() error {
	if err := t.owner.inject("COMMIT"); err != nil {
		// 真实数据库 commit 失败后事务同样不可用；先回滚底层事务，
		// 避免连接停留在打开的事务里影响后续重试。
		_ = t.tx.Rollback()
		return err
	}
	return t.tx.Commit()
}

func (t *faultTx) Rollback() error { return t.tx.Rollback() }

var (
	faultDriverMu    sync.Mutex
	faultDriverCount int
	baseSQLiteDriver driver.Driver
)

// newFaultDB 用唯一驱动名注册包装驱动，并打开该驱动下的独立内存库。
func newFaultDB(t *testing.T, dsn string) (*sql.DB, *faultDriver) {
	t.Helper()
	faultDriverMu.Lock()
	if baseSQLiteDriver == nil {
		probe, err := sql.Open(dialect.SQLite, dsn)
		if err != nil {
			faultDriverMu.Unlock()
			t.Fatal(err)
		}
		baseSQLiteDriver = probe.Driver()
	}
	base := baseSQLiteDriver
	faultDriverCount++
	name := fmt.Sprintf("kaguya-fault-%d-%s", faultDriverCount, t.Name())
	faultDriverMu.Unlock()

	fault := &faultDriver{base: base}
	sql.Register(name, fault)
	db, err := sql.Open(name, dsn)
	if err != nil {
		t.Fatalf("open fault sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db, fault
}

// setupFaultRecorderTest 建立带故障注入的记录器测试客户端，初始化流程与 setupChatTest 一致。
func setupFaultRecorderTest(t *testing.T) (context.Context, *ent.Client, *faultDriver) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, fault := newFaultDB(t, fmt.Sprintf("file:%s?mode=memory&cache=shared&_foreign_keys=on&_busy_timeout=5000", t.Name()))
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, conn)))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldID, oldLogger := db.EntClient, global.Id, global.Logger
	gen := &chatTestID{}
	gen.value.Store(123456789012340)
	db.EntClient, global.Id, global.Logger = client, gen, zap.NewNop()
	t.Cleanup(func() { db.EntClient, global.Id, global.Logger = oldClient, oldID, oldLogger })
	if err := initialize.Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetGlobalAgentsPaths([]string{}).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	return ctx, client, fault
}

// recordTextDelta 模拟模型在指定 step 推送一段正文。
func recordTextDelta(trace *turnTrace, step int, delta string) {
	call := newChatStream(func(dtochat.ContentBlock) error { return nil }).callbacks()
	trace.wrap(&call)
	if err := call.OnStepStart(step); err != nil {
		panic(err)
	}
	if err := call.OnTextStart("0"); err != nil {
		panic(err)
	}
	if err := call.OnTextDelta("0", delta); err != nil {
		panic(err)
	}
}

// startRecorderTurn 写入 running 占位行，返回未启动后台循环的 recorder。
func startRecorderTurn(t *testing.T, ctx context.Context, client *ent.Client) (*turnRecorder, string) {
	t.Helper()
	row, err := beginTurn(ctx, turnStart{
		ConversationID: "recorder", UserContent: "记录", StartedAt: time.Now(),
		ProviderID: "p", ProviderName: "p", ModelID: "m", ModelName: "m", APIProtocol: "openai-chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &turnRecorder{
		turnID: row.ID, trace: newTurnTrace(), done: make(chan struct{}),
		stopped: make(chan struct{}), flushedRev: make(map[int64]int64),
	}, row.ID
}

// 写入第二个块失败后，内存水位不能推进；重试必须补齐两个块且序列唯一。
func TestRecorderFlushRetriesAfterPartialWriteFailure(t *testing.T) {
	ctx, client, fault := setupFaultRecorderTest(t)
	recorder, turnID := startRecorderTurn(t, ctx, client)
	recordTextDelta(recorder.trace, 0, "first")
	recordTextDelta(recorder.trace, 1, "second")
	// 第一个 sequence 写入成功，第二个 sequence 失败，模拟事务中途失败。
	fault.failOnNth("INSERT INTO `kaguya_chat_block`", 2)
	if err := recorder.flush(ctx); err == nil {
		t.Fatal("flush must report the injected write failure")
	}
	blocks, err := client.KaguyaChatBlock.Query().Where(kaguyachatblock.TurnIDEQ(turnID)).All(ctx)
	if err != nil || len(blocks) != 0 {
		t.Fatalf("failed transaction left %d blocks: %v", len(blocks), err)
	}
	if len(recorder.flushedRev) != 0 || recorder.flushed.Revision != 0 {
		t.Fatalf("watermark advanced after failed commit: rev=%+v flushed=%+v", recorder.flushedRev, recorder.flushed)
	}

	// 重试必须重新写入全部块，且序列不重复。
	if err := recorder.flush(ctx); err != nil {
		t.Fatalf("retry flush: %v", err)
	}
	blocks, err = client.KaguyaChatBlock.Query().Where(kaguyachatblock.TurnIDEQ(turnID)).Order(kaguyachatblock.BySequence()).All(ctx)
	if err != nil || len(blocks) != 2 {
		t.Fatalf("retry blocks=%+v err=%v", blocks, err)
	}
	if blocks[0].Sequence == blocks[1].Sequence || blocks[0].Text != "first" || blocks[1].Text != "second" {
		t.Fatalf("retry lost content: %+v", blocks)
	}
	if recorder.shouldFlush() {
		t.Fatal("watermark should match the committed snapshot")
	}
}

// commit 失败时同样不能推进水位，重试写入的块内容必须是最新版本。
func TestRecorderFlushRetriesAfterCommitFailure(t *testing.T) {
	ctx, client, fault := setupFaultRecorderTest(t)
	recorder, turnID := startRecorderTurn(t, ctx, client)
	recordTextDelta(recorder.trace, 0, "answer")

	fault.failNext("COMMIT")
	if err := recorder.flush(ctx); err == nil {
		t.Fatal("flush must report the injected commit failure")
	}
	if len(recorder.flushedRev) != 0 || recorder.flushed.Revision != 0 {
		t.Fatalf("watermark advanced after failed commit: rev=%+v flushed=%+v", recorder.flushedRev, recorder.flushed)
	}
	recordTextDelta(recorder.trace, 0, "-more")
	if err := recorder.flush(ctx); err != nil {
		t.Fatalf("retry flush: %v", err)
	}
	blocks, err := client.KaguyaChatBlock.Query().Where(kaguyachatblock.TurnIDEQ(turnID)).All(ctx)
	if err != nil || len(blocks) != 1 || blocks[0].Text != "answer-more" {
		t.Fatalf("blocks=%+v err=%v", blocks, err)
	}
}

// 快照与统计必须来自同一临界区：旧实现分两次加锁会让 flush 期间新增的 delta
// 被误判为已刷。这里固定注入“快照后新增 delta”的顺序验证水位判断。
func TestRecorderSnapshotCoversConcurrentDelta(t *testing.T) {
	recorder := &turnRecorder{turnID: "turn", trace: newTurnTrace(), done: make(chan struct{}), stopped: make(chan struct{}), flushedRev: make(map[int64]int64)}
	recordTextDelta(recorder.trace, 0, "part")
	snapshot := recorder.trace.snapshot()
	// 快照之后模型继续推送，flush 尚未执行。
	recordTextDelta(recorder.trace, 0, "-next")
	recorder.mu.Lock()
	recorder.flushed = snapshot.traceStats
	recorder.mu.Unlock()
	if !recorder.shouldFlush() {
		t.Fatal("delta newer than the flushed snapshot must schedule another flush")
	}
}

// 已保存块被外部删除时，零行更新必须报错而不是静默成功。
func TestRecorderFlushFailsWhenBlockDisappeared(t *testing.T) {
	ctx, client, _ := setupFaultRecorderTest(t)
	recorder, turnID := startRecorderTurn(t, ctx, client)
	recordTextDelta(recorder.trace, 0, "answer")
	if err := recorder.flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.KaguyaChatBlock.Delete().Where(kaguyachatblock.TurnIDEQ(turnID)).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	recordTextDelta(recorder.trace, 0, "-more")
	err := recorder.flush(ctx)
	if err == nil {
		t.Fatal("flush must fail when the placeholder block no longer exists")
	}
	if !strings.Contains(err.Error(), "affected 0 rows") {
		t.Fatalf("unexpected error: %v", err)
	}
}
