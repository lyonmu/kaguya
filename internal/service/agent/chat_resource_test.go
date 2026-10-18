//go:build darwin || linux

package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/trace"
	"slices"
	"strconv"
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
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
	"github.com/lyonmu/kaguya/internal/ent/migrate"
	"github.com/lyonmu/kaguya/internal/global"
	initialize "github.com/lyonmu/kaguya/internal/init"
	"go.uber.org/zap"
)

var chatResource = flag.Bool("chat-resource", false, "measure three rounds of 16/32/64 SQLCipher chats, completion and cancellation")
var chatResourceDBMetadata = flag.Bool("chat-resource-db-metadata", false, "diagnose WAL metadata around commits; excluded from performance acceptance")
var chatResourceBaseline = flag.String("chat-resource-baseline", "", "compare every p95 group with the matching instrument's isolated HEAD JSON")
var chatResourceSyncFlush = flag.Bool("chat-resource-sync-flush", false, "diagnose a full concurrent flush wave without changing the natural-ticker gate")
var chatResourceTraceDir = flag.String("chat-resource-trace-dir", "", "write diagnostic scheduler traces to an existing private directory; incompatible with the performance gate")
var chatResourceOwners = flag.Bool("chat-resource-owners", false, "diagnose native connection owners and release bounds; excluded from performance acceptance")

// 夹具与仪器全部放在本文件，隔离HEAD可运行完全相同的代码；不把当前业务
// overlay到基线，不为测量增加产品hook或改动生产ticker/输出/池容量。
type chatResourceID struct{ value atomic.Int64 }

func (g *chatResourceID) GenID() (int64, error) { return g.value.Add(1), nil }

type chatResourceDriver struct {
	dialect.Driver
	mu              sync.Mutex
	incremental     []int64
	final           []int64
	flushWait       []int64
	finalWait       []int64
	flushPhases     []chatResourcePhases
	finalPhases     []chatResourcePhases
	notify          chan struct{}
	flushTarget     int64
	flushCount      atomic.Int64
	flushReady      chan struct{}
	flushGate       chan struct{}
	gateOnce        sync.Once
	capture         atomic.Bool
	traceOn         bool
	traceSeq        atomic.Uint64
	walPath         string
	dbMetadata      bool
	statementCounts map[string]int
	owners          *chatOwnerProbe
}

// Diagnostic labels are a fixed table allowlist plus operation, never SQL/args.
// Counts include attempts; Query may defer sqlite3_step to Rows.Next.
func (d *chatResourceDriver) countStatement(query string) {
	if !d.dbMetadata || !d.capture.Load() {
		return
	}
	operation := "other"
	for _, candidate := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
		if strings.HasPrefix(query, candidate+" ") {
			operation = candidate
			break
		}
	}
	table := "other"
	for _, candidate := range []string{"kaguya_system_info", "kaguya_provider_info", "kaguya_models_info", "kaguya_conversation", "kaguya_chat_turn", "kaguya_chat_block", "kaguya_title_job", "kaguya_approval", "kaguya_task_usage"} {
		if strings.Contains(query, "`"+candidate+"`") || strings.Contains(query, `"`+candidate+`"`) {
			table = candidate
			break
		}
	}
	d.mu.Lock()
	if d.statementCounts == nil {
		d.statementCounts = map[string]int{}
	}
	d.statementCounts[operation+":"+table]++
	d.mu.Unlock()
}

func (d *chatResourceDriver) Exec(ctx context.Context, query string, args, value any) error {
	d.countStatement(query)
	ctx, op := d.startOwner(ctx, "exec", nil)
	err := d.Driver.Exec(ctx, query, args, value)
	op.returned(err, true)
	return err
}

func (d *chatResourceDriver) Query(ctx context.Context, query string, args, value any) error {
	d.countStatement(query)
	ctx, op := d.startOwner(ctx, "query", nil)
	err := d.Driver.Query(ctx, query, args, value)
	op.returned(err, err != nil)
	wrapChatOwnerRows(value, op, err)
	return err
}

func (d *chatResourceDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	// Atlas迁移直接断言底层*sql.Rows，资源计时仅在fixture已准备好后开启；
	// 初始化仍用真实原生driver，不在其兼容接口里插入Rows装饰器。
	if !d.capture.Load() {
		return d.Driver.Tx(ctx)
	}
	if d.flushGate != nil && d.flushCount.Load() < d.flushTarget {
		// 仅测试driver在真实recorder到达Tx入口时等待；不替换ticker、SQL或
		// 生产函数。所有N个到达后同波竞争连接，仪器等待不计入DB计时。
		var pcs [16]uintptr
		n := runtime.Callers(2, pcs[:])
		frames := runtime.CallersFrames(pcs[:n])
		for {
			frame, more := frames.Next()
			if strings.HasSuffix(frame.Function, ".(*turnRecorder).flush") {
				if d.flushCount.Add(1) <= d.flushTarget {
					d.flushReady <- struct{}{}
					select {
					case <-d.flushGate:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				break
			}
			if !more {
				break
			}
		}
	}
	var traceCtx context.Context
	var task *trace.Task
	var traceID uint64
	if d.traceOn || d.dbMetadata {
		traceID = d.traceSeq.Add(1)
	}
	if d.traceOn {
		traceCtx, task = trace.NewTask(ctx, "chat-db-transaction")
		trace.Logf(traceCtx, "instrument-id", "%d", traceID)
	}
	var region *trace.Region
	if task != nil {
		region = trace.StartRegion(traceCtx, "connection-and-begin")
	}
	started := time.Now() // 包含database/sql等待单连接的时间。
	ctx, ownerOperation := d.startOwner(ctx, "tx", nil)
	if task != nil && ownerOperation != nil {
		trace.Logf(traceCtx, "owner-id", "%d", ownerOperation.id())
	}
	tx, err := d.Driver.Tx(ctx)
	ownerOperation.returned(err, err != nil)
	beginNS := time.Since(started).Nanoseconds()
	if region != nil {
		region.End()
	}
	if err != nil {
		if task != nil {
			trace.Log(traceCtx, "outcome", "begin-failed")
			task.End()
		}
		return nil, err
	}
	return &chatResourceTx{Tx: tx, owner: d, started: started, beginNS: beginNS, traceCtx: traceCtx, traceTask: task, traceID: traceID, ownerOperation: ownerOperation}, nil
}

type chatResourceTx struct {
	dialect.Tx
	owner          *chatResourceDriver
	started        time.Time
	beginNS        int64 // 连接获取及实际BEGIN；不是纯pool等待，也不相减p95。
	blockWrite     bool
	turnWrite      bool
	statementsNS   int64
	rowsNS         int64
	statements     int
	traceCtx       context.Context
	traceTask      *trace.Task
	traceID        uint64
	traceEndOnce   sync.Once
	ownerOperation *chatOwnerOperation
}

func (tx *chatResourceTx) traceRegion(name string) *trace.Region {
	if tx.traceTask == nil {
		return nil
	}
	return trace.StartRegion(tx.traceCtx, name)
}

func (tx *chatResourceTx) Rollback() error {
	err := tx.Tx.Rollback()
	if !errors.Is(err, sql.ErrTxDone) {
		tx.ownerOperation.releaseUpper()
	}
	if tx.traceTask != nil {
		tx.traceEndOnce.Do(tx.traceTask.End)
	}
	return err
}

// QueryContext返回并不保证SQLite已完成执行，RETURNING的sqlite3_step可能在
// Next中发生；单独记录Rows调用，不能把它当作纯Ent内存处理。
type chatResourcePhases struct {
	OwnerID      uint64           `json:"owner_id,omitempty"`
	TotalNS      int64            `json:"total_ns"`
	BeginNS      int64            `json:"begin_ns"`
	StatementsNS int64            `json:"statement_calls_ns"`
	RowsNS       int64            `json:"rows_calls_ns"`
	CommitNS     int64            `json:"commit_ns"`
	OtherNS      int64            `json:"other_ns"`
	Statements   int              `json:"statements"`
	TraceID      uint64           `json:"trace_id,omitempty"`
	WALBefore    *chatResourceWAL `json:"wal_before,omitempty"`
	WALAfter     *chatResourceWAL `json:"wal_after,omitempty"`
}

func (tx *chatResourceTx) observe(query string) {
	if !strings.HasPrefix(query, "INSERT ") && !strings.HasPrefix(query, "UPDATE ") && !strings.HasPrefix(query, "DELETE ") {
		return
	}
	// 只识别固定表名与写操作，不读取/输出SQL参数或正文。增量flush只有block
	// 写入，整轮事务同时写turn终态；会话/标题/只读事务不纳入flush样本。
	tx.blockWrite = tx.blockWrite || strings.Contains(query, "`kaguya_chat_block`") || strings.Contains(query, `"kaguya_chat_block"`)
	tx.turnWrite = tx.turnWrite || strings.Contains(query, "`kaguya_chat_turn`") || strings.Contains(query, `"kaguya_chat_turn"`)
}

func (tx *chatResourceTx) Exec(ctx context.Context, query string, args, value any) error {
	tx.owner.countStatement(query)
	tx.observe(query)
	region := tx.traceRegion("exec")
	started := time.Now()
	ctx, op := tx.owner.startOwner(ctx, "exec", tx.ownerOperation)
	err := tx.Tx.Exec(ctx, query, args, value)
	op.returned(err, false)
	tx.statementsNS += time.Since(started).Nanoseconds()
	if region != nil {
		region.End()
	}
	tx.statements++
	return err
}

func (tx *chatResourceTx) Query(ctx context.Context, query string, args, value any) error {
	tx.owner.countStatement(query)
	tx.observe(query)
	region := tx.traceRegion("query")
	started := time.Now()
	ctx, op := tx.owner.startOwner(ctx, "query", tx.ownerOperation)
	err := tx.Tx.Query(ctx, query, args, value)
	op.returned(err, false)
	wrapChatOwnerRows(value, op, err)
	tx.statementsNS += time.Since(started).Nanoseconds()
	if region != nil {
		region.End()
	}
	tx.statements++
	if err == nil {
		if rows, ok := value.(*entsql.Rows); ok && rows.ColumnScanner != nil {
			rows.ColumnScanner = &chatResourceRows{ColumnScanner: rows.ColumnScanner, tx: tx}
		}
	}
	return err
}

type chatResourceRows struct {
	entsql.ColumnScanner
	tx *chatResourceTx
}

func (r *chatResourceRows) addTime(started time.Time) {
	r.tx.rowsNS += time.Since(started).Nanoseconds()
}
func (r *chatResourceRows) Next() bool {
	if region := r.tx.traceRegion("rows"); region != nil {
		defer region.End()
	}
	defer r.addTime(time.Now())
	return r.ColumnScanner.Next()
}
func (r *chatResourceRows) NextResultSet() bool {
	if region := r.tx.traceRegion("rows"); region != nil {
		defer region.End()
	}
	defer r.addTime(time.Now())
	return r.ColumnScanner.NextResultSet()
}
func (r *chatResourceRows) Scan(dest ...any) error {
	if region := r.tx.traceRegion("rows"); region != nil {
		defer region.End()
	}
	defer r.addTime(time.Now())
	return r.ColumnScanner.Scan(dest...)
}
func (r *chatResourceRows) Close() error {
	if region := r.tx.traceRegion("rows"); region != nil {
		defer region.End()
	}
	defer r.addTime(time.Now())
	return r.ColumnScanner.Close()
}
func (r *chatResourceRows) Columns() ([]string, error) {
	if region := r.tx.traceRegion("rows"); region != nil {
		defer region.End()
	}
	defer r.addTime(time.Now())
	return r.ColumnScanner.Columns()
}
func (r *chatResourceRows) ColumnTypes() ([]*sql.ColumnType, error) {
	if region := r.tx.traceRegion("rows"); region != nil {
		defer region.End()
	}
	defer r.addTime(time.Now())
	return r.ColumnScanner.ColumnTypes()
}
func (r *chatResourceRows) Err() error {
	if region := r.tx.traceRegion("rows"); region != nil {
		defer region.End()
	}
	defer r.addTime(time.Now())
	return r.ColumnScanner.Err()
}

func (tx *chatResourceTx) Commit() error {
	var before, after *chatResourceWAL
	if tx.owner.dbMetadata {
		before = readChatResourceWAL(tx.owner.walPath)
	}
	region := tx.traceRegion("commit")
	started := time.Now()
	err := tx.Tx.Commit()
	if !errors.Is(err, sql.ErrTxDone) {
		tx.ownerOperation.releaseUpper()
	}
	commitNS := time.Since(started).Nanoseconds()
	if region != nil {
		region.End()
	}
	elapsed := time.Since(tx.started).Nanoseconds()
	if tx.owner.dbMetadata {
		after = readChatResourceWAL(tx.owner.walPath)
	}
	if tx.traceTask != nil {
		kind := "other"
		if tx.blockWrite {
			kind = "incremental-flush"
			if tx.turnWrite {
				kind = "final-save"
			}
		}
		trace.Log(tx.traceCtx, "transaction-kind", kind)
		trace.Logf(tx.traceCtx, "commit-ns", "%d", commitNS)
		if err != nil {
			trace.Log(tx.traceCtx, "outcome", "commit-failed")
		}
		tx.traceEndOnce.Do(tx.traceTask.End)
	}
	if err == nil && tx.blockWrite {
		phases := chatResourcePhases{TotalNS: elapsed, BeginNS: tx.beginNS, StatementsNS: tx.statementsNS, RowsNS: tx.rowsNS, CommitNS: commitNS, Statements: tx.statements, TraceID: tx.traceID, WALBefore: before, WALAfter: after, OwnerID: tx.ownerOperation.id()}
		phases.OtherNS = elapsed - phases.BeginNS - phases.StatementsNS - phases.RowsNS - phases.CommitNS
		tx.owner.mu.Lock()
		if tx.turnWrite {
			tx.owner.final = append(tx.owner.final, elapsed)
			tx.owner.finalWait = append(tx.owner.finalWait, tx.beginNS)
			tx.owner.finalPhases = append(tx.owner.finalPhases, phases)
		} else {
			tx.owner.incremental = append(tx.owner.incremental, elapsed)
			tx.owner.flushWait = append(tx.owner.flushWait, tx.beginNS)
			tx.owner.flushPhases = append(tx.owner.flushPhases, phases)
		}
		tx.owner.mu.Unlock()
		select {
		case tx.owner.notify <- struct{}{}:
		default:
		}
	}
	return err
}

type chatResourceSample struct {
	HeapBytes  uint64 `json:"heap_bytes"`
	RSSBytes   uint64 `json:"rss_bytes"`
	FDs        int    `json:"fds"`
	Goroutines int    `json:"goroutines"`
}

func sampleChatResources(t *testing.T) chatResourceSample {
	t.Helper()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	fdPath := "/dev/fd"
	if runtime.GOOS == "linux" {
		fdPath = "/proc/self/fd"
	}
	fds, err := os.ReadDir(fdPath)
	if err != nil {
		t.Errorf("cannot measure descriptors: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/bin/ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Errorf("cannot measure resident memory: %v", err)
	}
	rss, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		t.Errorf("invalid resident memory measurement: %v", err)
	}
	return chatResourceSample{memory.HeapAlloc, rss * 1024, len(fds), runtime.NumGoroutine()}
}

func (s *chatResourceSample) include(next chatResourceSample) {
	s.HeapBytes = max(s.HeapBytes, next.HeapBytes)
	s.RSSBytes = max(s.RSSBytes, next.RSSBytes)
	s.FDs = max(s.FDs, next.FDs)
	s.Goroutines = max(s.Goroutines, next.Goroutines)
}

// Read-only fixture diagnostics. Header bytes are never returned or logged.
// File/header observations can race the next writer after Commit; they are not
// proof of this transaction's fsync/checkpoint cost or its logical frame count.
type chatResourceWAL struct {
	Exists             bool   `json:"exists"`
	Bytes              int64  `json:"bytes"`
	PageSize           uint32 `json:"page_size,omitempty"`
	CheckpointSequence uint32 `json:"checkpoint_sequence,omitempty"`
	HeaderStable       bool   `json:"header_stable"`
	ReadFailed         bool   `json:"read_failed,omitempty"`
}

func readChatResourceWAL(path string) *chatResourceWAL {
	result := &chatResourceWAL{}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return result
	}
	if err != nil {
		result.ReadFailed = true
		return result
	}
	defer file.Close()
	result.Exists = true
	info, err := file.Stat()
	if err != nil {
		result.ReadFailed = true
		return result
	}
	result.Bytes = info.Size()
	if result.Bytes < 32 {
		return result
	}
	var first, second [32]byte
	if _, err := io.ReadFull(file, first[:]); err != nil {
		result.ReadFailed = true
		return result
	}
	if _, err := file.ReadAt(second[:], 0); err != nil {
		result.ReadFailed = true
		return result
	}
	magic := binary.BigEndian.Uint32(first[:4])
	result.HeaderStable = first == second && (magic == 0x377f0682 || magic == 0x377f0683)
	if result.HeaderStable {
		result.PageSize = binary.BigEndian.Uint32(first[8:12])
		result.CheckpointSequence = binary.BigEndian.Uint32(first[12:16])
	}
	return result
}

type chatResourceDBSettings struct {
	JournalMode         string `json:"journal_mode"`
	Synchronous         int    `json:"synchronous"`
	PageSize            int    `json:"page_size"`
	WALAutoCheckpoint   int    `json:"wal_autocheckpoint"`
	FullFSync           int    `json:"fullfsync"`
	CheckpointFullFSync int    `json:"checkpoint_fullfsync"`
	ForeignKeys         int    `json:"foreign_keys"`
	BusyTimeout         int    `json:"busy_timeout_ms"`
	PageCount           int    `json:"page_count"`
	FreelistCount       int    `json:"freelist_count"`
	PoolMaxOpen         int    `json:"pool_max_open"`
}

func readChatResourceDBSettings(t *testing.T, ctx context.Context, pool *sql.DB) chatResourceDBSettings {
	t.Helper()
	settings := chatResourceDBSettings{PoolMaxOpen: pool.Stats().MaxOpenConnections}
	if err := pool.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&settings.JournalMode); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name  string
		value *int
	}{
		{"synchronous", &settings.Synchronous}, {"page_size", &settings.PageSize},
		{"wal_autocheckpoint", &settings.WALAutoCheckpoint}, {"fullfsync", &settings.FullFSync},
		{"checkpoint_fullfsync", &settings.CheckpointFullFSync}, {"foreign_keys", &settings.ForeignKeys},
		{"busy_timeout", &settings.BusyTimeout}, {"page_count", &settings.PageCount}, {"freelist_count", &settings.FreelistCount},
	} {
		if err := pool.QueryRowContext(ctx, "PRAGMA "+item.name).Scan(item.value); err != nil {
			t.Fatal(err)
		}
	}
	if settings.JournalMode != "wal" || settings.Synchronous != 2 || settings.ForeignKeys != 1 || settings.PoolMaxOpen != 1 {
		t.Fatal("resource fixture changed SQLite durability/isolation configuration")
	}
	return settings
}

type chatResourceResult struct {
	OwnersEnabled     bool                   `json:"owners_enabled,omitempty"`
	OwnerRecords      []chatOwnerRecord      `json:"owner_records,omitempty"`
	StatementCounts   map[string]int         `json:"statement_counts,omitempty"`
	Phase             string                 `json:"phase"`
	SyncFlush         bool                   `json:"sync_flush"`
	TraceEnabled      bool                   `json:"trace_enabled,omitempty"`
	DBMetadataEnabled bool                   `json:"db_metadata_enabled,omitempty"`
	DBSettings        chatResourceDBSettings `json:"db_settings"`
	WALPrepared       *chatResourceWAL       `json:"wal_prepared"`
	WALAfterFlush     *chatResourceWAL       `json:"wal_after_flush"`
	WALAfterDone      *chatResourceWAL       `json:"wal_after_done"`
	Chats             int                    `json:"chats"`
	Round             int                    `json:"round"`
	ElapsedMS         float64                `json:"elapsed_ms"`
	Before            chatResourceSample     `json:"before"`
	BeforeWork        chatResourceSample     `json:"before_work"`
	ObservedPeak      chatResourceSample     `json:"observed_peak"`
	AfterDone         chatResourceSample     `json:"after_done"`
	AfterCleanup      chatResourceSample     `json:"after_cleanup"`
	AdmissionNS       []int64                `json:"admission_ns"`
	IncrementalNS     []int64                `json:"incremental_flush_ns"`
	FinalNS           []int64                `json:"final_save_ns"`
	FlushWaitNS       []int64                `json:"flush_tx_begin_ns"`
	FinalWaitNS       []int64                `json:"final_tx_begin_ns"`
	FlushPhases       []chatResourcePhases   `json:"flush_phases"`
	FinalPhases       []chatResourcePhases   `json:"final_phases"`
	ModelCalls        int64                  `json:"model_calls"`
	MaxInflight       int64                  `json:"max_model_inflight"`
	ActiveModels      int64                  `json:"active_models_after_cleanup"`
	PendingWork       int64                  `json:"pending_work_after_done"`
	Leases            int                    `json:"leases_after_done"`
	PoolWaitCount     int64                  `json:"pool_wait_count"`
	PoolWaitNS        int64                  `json:"pool_wait_ns"`
	PoolInUse         int                    `json:"pool_in_use_after_done"`
	DoneFrames        int                    `json:"done_frames"`
	CanceledTurns     int                    `json:"canceled_turns"`
	Interrupted       int                    `json:"interrupted_turns"`
}

func openChatResource(t *testing.T, chats int) (context.Context, *ent.Client, *sql.DB, *chatResourceDriver, *TitleQueue) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := config.DatabaseConfig{Path: filepath.Join(home, "resource.db"), KeyFile: filepath.Join(home, "key")}
	if err := os.WriteFile(cfg.KeyFile, []byte(strings.Repeat("ab", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	dsn, err := cfg.SQLiteDSN()
	if err != nil {
		t.Fatal(err)
	}
	var owners *chatOwnerProbe
	var pool *sql.DB
	if *chatResourceOwners {
		owners = &chatOwnerProbe{}
		pool = sql.OpenDB(chatOwnerConnector{dsn: dsn, probe: owners})
	} else {
		pool, err = sql.Open("sqlite3", dsn)
	}
	if err != nil {
		t.Fatal("cannot open resource database")
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	probe := &chatResourceDriver{Driver: entsql.OpenDB(dialect.SQLite, pool), notify: make(chan struct{}, 512), walPath: cfg.Path + "-wal", dbMetadata: *chatResourceDBMetadata, owners: owners}
	if *chatResourceSyncFlush {
		probe.flushTarget, probe.flushReady, probe.flushGate = int64(chats), make(chan struct{}, chats), make(chan struct{})
		t.Cleanup(func() { probe.gateOnce.Do(func() { close(probe.flushGate) }) })
	}
	client := ent.NewClient(ent.Driver(probe))
	t.Cleanup(func() { _ = client.Close() })
	var cipher string
	if err := pool.QueryRowContext(ctx, "PRAGMA cipher_version").Scan(&cipher); err != nil || cipher == "" {
		t.Fatal("chat resource fixture requires SQLCipher")
	}
	if err := client.Schema.Create(ctx, migrate.WithForeignKeys(false)); err != nil {
		t.Fatal(err)
	}
	oldClient, oldID, oldLogger := db.EntClient, global.Id, global.Logger
	oldQueue, oldPublisher, oldInstructions := DefaultTitleQueue, DefaultTitlePublisher, DefaultGlobalInstructions
	gen := &chatResourceID{}
	gen.value.Store(123456789012340)
	db.EntClient, global.Id, global.Logger = client, gen, zap.NewNop()
	DefaultTitlePublisher, DefaultGlobalInstructions = NewTitlePublisher(), nil
	t.Cleanup(func() {
		DefaultTitlePublisher.Close()
		db.EntClient, global.Id, global.Logger = oldClient, oldID, oldLogger
		DefaultTitleQueue, DefaultTitlePublisher, DefaultGlobalInstructions = oldQueue, oldPublisher, oldInstructions
	})
	if err := initialize.Run(ctx, client); err != nil {
		t.Fatal(err)
	}
	if err := client.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetGlobalAgentsPaths([]string{}).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	queue := NewTitleQueue(ctx, client, global.Logger)
	DefaultTitleQueue = queue
	queue.Start() // 与runtime一样持有真实单worker；未配置任务模型，不发标题HTTP。
	t.Cleanup(func() { queue.Close(); <-queue.Done() })
	return ctx, client, pool, probe, queue
}

func TestChatAdmissionAndFlushResources(t *testing.T) {
	if !*chatResource {
		t.Skip("run with -chat-resource to collect the release resource gate")
	}
	if (*chatResourceTraceDir != "" || *chatResourceDBMetadata || *chatResourceOwners) && *chatResourceBaseline != "" {
		t.Fatal("diagnostic traces/WAL reads cannot be used for the untraced performance gate")
	}
	var results []chatResourceResult
	for _, chats := range []int{16, 32, 64} {
		for _, phase := range []string{"completed", "canceled_shutdown"} {
			for round := 1; round <= 3; round++ {
				runtime.GC()
				result := chatResourceResult{Phase: phase, SyncFlush: *chatResourceSyncFlush, TraceEnabled: *chatResourceTraceDir != "", DBMetadataEnabled: *chatResourceDBMetadata, OwnersEnabled: *chatResourceOwners, Chats: chats, Round: round, Before: sampleChatResources(t)}
				ran := false
				t.Run(fmt.Sprintf("%s_%d_round_%d", phase, chats, round), func(t *testing.T) {
					ran = true
					runChatResource(t, &result)
				}) // fixture cleanup含queue/HTTP/DB关闭后再测归零。
				if !ran {
					continue
				}
				runtime.GC()
				result.AfterCleanup = sampleChatResources(t)
				if result.AfterCleanup.FDs != result.Before.FDs {
					t.Errorf("%s/%d did not close descriptors: before=%d cleanup=%d", phase, chats, result.Before.FDs, result.AfterCleanup.FDs)
				}
				encoded, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("CHAT_RESOURCE %s", encoded)
				results = append(results, result)
			}
		}
	}
	if *chatResourceBaseline != "" && !t.Failed() {
		checkChatResourceP95(t, results, *chatResourceBaseline)
	}
}

func runChatResource(t *testing.T, result *chatResourceResult) {
	t.Helper()
	ctx, client, pool, probe, queue := openChatResource(t, result.Chats)
	activeWork.mu.Lock()
	oldStopping := activeWork.stopping
	activeWork.stopping = false
	activeWork.mu.Unlock()
	t.Cleanup(func() { activeWork.mu.Lock(); activeWork.stopping = oldStopping; activeWork.mu.Unlock() })
	if PendingWork() != 0 {
		t.Fatal("resource fixture started with active work")
	}
	var active, peak, calls atomic.Int64
	entered := make(chan int, result.Chats)
	admission := make([]int64, result.Chats)
	requestStart := make([]time.Time, result.Chats)
	var timingsMu sync.Mutex
	gate, launch := make(chan struct{}), make(chan struct{})
	var gateOnce sync.Once
	responseText := strings.Repeat("resource-answer-", 2048) // 32KiB，触发既有16KiBflush条件。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.Stream || len(body.Messages) == 0 {
			t.Error("unexpected mock request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		index, err := strconv.Atoi(body.Messages[len(body.Messages)-1].Content)
		if err != nil || index < 0 || index >= result.Chats {
			t.Error("mock request lost conversation identity")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		timingsMu.Lock()
		admission[index] = time.Since(requestStart[index]).Nanoseconds()
		timingsMu.Unlock()
		calls.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); current > previous && !peak.CompareAndSwap(previous, current); previous = peak.Load() {
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"id\":\"resource\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":null}]}\n\n", responseText)
		w.(http.Flusher).Flush()
		entered <- index
		select {
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		case <-gate:
			fmt.Fprint(w, "data: {\"id\":\"resource\",\"object\":\"chat.completion.chunk\",\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	}))
	t.Cleanup(func() { gateOnce.Do(func() { close(gate) }); server.CloseClientConnections(); server.Close() })
	provider, err := client.KaguyaProviderInfo.Create().SetProviderName("resource").SetAPIKey("fixture").SetBaseURL(server.URL).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model, err := client.KaguyaModelsInfo.Create().SetProviderID(provider.ID).SetModelName("test").SetModelID("test").SetRequestPath("/v1/chat/completions").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// PRAGMA只在负载前读取实际值，不调用任何checkpoint或改变连接参数。
	result.DBSettings = readChatResourceDBSettings(t, ctx, pool)
	result.WALPrepared = readChatResourceWAL(probe.walPath)
	probe.capture.Store(true)
	finishTrace := startChatResourceTrace(t, result)
	defer finishTrace()
	probe.traceOn = result.TraceEnabled
	result.BeforeWork = sampleChatResources(t)
	result.ObservedPeak = result.BeforeWork
	var samplesMu sync.Mutex
	stopSamples, samplesDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(samplesDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopSamples:
				return
			case <-ticker.C:
				sample := sampleChatResources(t)
				samplesMu.Lock()
				result.ObservedPeak.include(sample)
				samplesMu.Unlock()
			}
		}
	}()
	var stopSamplesOnce sync.Once
	finishSamples := func() { stopSamplesOnce.Do(func() { close(stopSamples); <-samplesDone }) }
	defer finishSamples()
	var workers sync.WaitGroup
	cancels := make([]context.CancelFunc, result.Chats)
	doneCounts := make([]int, result.Chats)
	frameErrors := make([]error, result.Chats)
	for index := range result.Chats {
		callCtx, cancel := context.WithCancel(ctx)
		cancels[index] = cancel
		frames := make(chan *dtochat.ChatResp, 64)
		workers.Add(2)
		go func() {
			defer workers.Done()
			<-launch
			timingsMu.Lock()
			requestStart[index] = time.Now()
			timingsMu.Unlock()
			(&AgentSvc{}).Chat(callCtx, frames, &dtochat.ChatReq{ID: fmt.Sprintf("resource-%03d", index), ModelID: model.ID, Messages: strconv.Itoa(index)})
		}()
		go func() {
			defer workers.Done()
			for frame := range frames {
				if frame.Err != nil {
					frameErrors[index] = frame.Err
				}
				if frame.Chat.Flag == dtochat.ChatFlagDone {
					doneCounts[index]++
				}
			}
		}()
	}
	defer func() {
		if probe.flushGate != nil {
			probe.gateOnce.Do(func() { close(probe.flushGate) })
		}
		for _, cancel := range cancels {
			cancel()
		}
		gateOnce.Do(func() { close(gate) })
		workers.Wait()
	}()
	statsBefore := pool.Stats()
	started := time.Now()
	close(launch)
	seen := make(map[int]bool, result.Chats)
	for len(seen) < result.Chats {
		select {
		case index := <-entered:
			if seen[index] {
				t.Fatal("duplicate model request")
			}
			seen[index] = true
		case <-ctx.Done():
			t.Fatalf("only %d/%d chats entered the model", len(seen), result.Chats)
		}
	}
	if peak.Load() != int64(result.Chats) || active.Load() != int64(result.Chats) {
		t.Fatal("model admissions were serialized or rejected")
	}
	busy := make(chan *dtochat.ChatResp, 16)
	(&AgentSvc{}).Chat(ctx, busy, &dtochat.ChatReq{ID: "resource-000", ModelID: model.ID, Messages: "duplicate"})
	frame := <-busy
	if frame == nil || !errors.Is(frame.Err, ErrConversationBusy) {
		t.Fatal("same conversation lost its busy guard")
	}
	for range busy {
		t.Fatal("busy request emitted extra frames")
	}
	if probe.flushGate != nil {
		for range result.Chats {
			select {
			case <-probe.flushReady:
			case <-ctx.Done():
				t.Fatal("recorders did not reach the synchronized flush gate")
			}
		}
		probe.gateOnce.Do(func() { close(probe.flushGate) })
	}
	// 等实际Recorder事务commit，不靠sleep推测tick；再检查所有真实block正文。
	for {
		probe.mu.Lock()
		flushed := len(probe.incremental)
		probe.mu.Unlock()
		if flushed >= result.Chats {
			break
		}
		select {
		case <-probe.notify:
		case <-ctx.Done():
			t.Fatalf("only %d/%d incremental commits", flushed, result.Chats)
		}
	}
	blocks, err := client.KaguyaChatBlock.Query().All(ctx)
	if err != nil || len(blocks) != result.Chats {
		t.Fatalf("incremental commits did not save all blocks: count=%d err=%v", len(blocks), err)
	}
	for _, block := range blocks {
		if block.Text != responseText {
			t.Fatal("incremental content differs from actual emitted text")
		}
	}
	if result.DBMetadataEnabled {
		result.WALAfterFlush = readChatResourceWAL(probe.walPath)
	}
	if result.Phase == "completed" {
		gateOnce.Do(func() { close(gate) })
	} else {
		activeWork.stop()
		stopped := make(chan *dtochat.ChatResp, 16)
		(&AgentSvc{}).Chat(ctx, stopped, &dtochat.ChatReq{ID: "stopping", ModelID: model.ID, Messages: "new"})
		frame := <-stopped
		if frame == nil || !errors.Is(frame.Err, ErrServiceStopping) {
			t.Fatal("shutdown admitted new work")
		}
		(&AgentSvc{}).StopConversation("resource-000")
		for _, cancel := range cancels {
			cancel()
		}
	}
	workers.Wait()
	queue.Close()
	select {
	case <-queue.Done():
	case <-ctx.Done():
		t.Fatal("title worker did not drain")
	}
	if err := WaitActive(ctx); err != nil {
		t.Fatal(err)
	}
	for index, count := range doneCounts {
		result.DoneFrames += count
		if result.Phase == "completed" && (count != 1 || frameErrors[index] != nil) || result.Phase != "completed" && count != 0 {
			t.Fatalf("invalid terminal frames: conversation=%d done=%d err=%v", index, count, frameErrors[index])
		}
	}
	turns, err := client.KaguyaChatTurn.Query().All(ctx)
	if err != nil || len(turns) != result.Chats {
		t.Fatalf("terminal turns: count=%d err=%v", len(turns), err)
	}
	for _, turn := range turns {
		switch turn.Status {
		case kaguyachatturn.StatusCompleted:
			if result.Phase != "completed" {
				t.Fatal("canceled turn completed")
			}
		case kaguyachatturn.StatusCanceled:
			result.CanceledTurns++
		case kaguyachatturn.StatusInterrupted:
			result.Interrupted++
		default:
			t.Fatalf("turn did not settle: %s", turn.Status)
		}
	}
	if result.Phase != "completed" && (result.CanceledTurns != 1 || result.Interrupted != result.Chats-1) {
		t.Fatalf("cancellation attribution: canceled=%d interrupted=%d", result.CanceledTurns, result.Interrupted)
	}
	server.CloseClientConnections()
	server.Close() // 所有mock handler先结束，active指标才代表释放。
	result.ElapsedMS = float64(time.Since(started)) / float64(time.Millisecond)
	if result.DBMetadataEnabled {
		result.WALAfterDone = readChatResourceWAL(probe.walPath)
	}
	result.AdmissionNS = slices.Clone(admission)
	probe.mu.Lock()
	if probe.statementCounts != nil {
		result.StatementCounts = make(map[string]int, len(probe.statementCounts))
		for label, count := range probe.statementCounts {
			result.StatementCounts[label] = count
		}
	}
	result.IncrementalNS, result.FinalNS = slices.Clone(probe.incremental), slices.Clone(probe.final)
	result.FlushWaitNS, result.FinalWaitNS = slices.Clone(probe.flushWait), slices.Clone(probe.finalWait)
	result.FlushPhases, result.FinalPhases = slices.Clone(probe.flushPhases), slices.Clone(probe.finalPhases)
	probe.mu.Unlock()
	result.OwnerRecords = probe.owners.snapshot()
	if len(result.IncrementalNS) < result.Chats || result.Phase == "completed" && len(result.FinalNS) != result.Chats {
		t.Fatal("timing probe did not cover actual flush and final commits")
	}
	result.ModelCalls, result.MaxInflight, result.ActiveModels = calls.Load(), peak.Load(), active.Load()
	result.PendingWork = PendingWork()
	activeConversations.Lock()
	result.Leases = len(activeConversations.leases)
	activeConversations.Unlock()
	statsAfter := pool.Stats()
	result.PoolInUse, result.PoolWaitCount, result.PoolWaitNS = statsAfter.InUse, statsAfter.WaitCount-statsBefore.WaitCount, (statsAfter.WaitDuration - statsBefore.WaitDuration).Nanoseconds()
	if result.ModelCalls != int64(result.Chats) || result.ActiveModels != 0 || result.PendingWork != 0 || result.Leases != 0 || result.PoolInUse != 0 {
		t.Fatalf("resource owners did not settle: %+v", result)
	}
	finishSamples()
	finishTrace()                             // 端点不包含trace writer句柄及其写入worker。
	result.AfterDone = sampleChatResources(t) // 已join采样子进程，端点不混入仪器FD。
	result.ObservedPeak.include(result.AfterDone)
}

// 只接受已存在的私有诊断目录，独立文件O_EXCL，避免覆盖已有记录。固定文件名
// 和trace标签不含请求正文/参数/凭据；重度诊断不参与无trace性能签收。
func startChatResourceTrace(t *testing.T, result *chatResourceResult) func() {
	t.Helper()
	if !result.TraceEnabled {
		return func() {}
	}
	dir, err := os.Stat(*chatResourceTraceDir)
	if err != nil || !dir.IsDir() || dir.Mode().Perm()&0077 != 0 || !filepath.IsAbs(*chatResourceTraceDir) {
		t.Fatal("trace directory must exist, be absolute and private (0700)")
	}
	mode := "natural"
	if result.SyncFlush {
		mode = "sync"
	}
	path := filepath.Join(*chatResourceTraceDir, fmt.Sprintf("%s_%s_%d_round_%d.trace", mode, result.Phase, result.Chats, result.Round))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := trace.Start(file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			trace.Stop()
			if err := file.Sync(); err != nil {
				t.Error(err)
			}
			if err := file.Close(); err != nil {
				t.Error(err)
			}
			t.Logf("CHAT_DIAGNOSTIC_TRACE %s", path)
		})
	}
}

type chatResourceBaselineDocument struct {
	Version          int                  `json:"version"`
	InstrumentSHA256 string               `json:"instrument_sha256"`
	Rows             []chatResourceResult `json:"rows"`
}

// v2覆盖同目录两个实际仪器源码；只核验调用文件会漏掉connector变化。
// sha256(sorted basename TAB file-sha256 LF)，不接受旧单文件SHA的基线。
func chatResourceInstrumentSHA256(t *testing.T) string {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot identify the instrument source")
	}
	hash := sha256.New()
	for _, name := range []string{"chat_resource_owner_test.go", "chat_resource_test.go"} {
		source, err := os.ReadFile(filepath.Join(filepath.Dir(sourcePath), name))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(hash, "%s\t%x\n", name, sha256.Sum256(source))
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

// 性能门槛是独立的退出条件；不提供基线只证明行为负载通过。基线必须保存
// 同一仪器全部18轮，不允许删除失败/较慢样本或漏掉一个phase后声称通过。
func checkChatResourceP95(t *testing.T, current []chatResourceResult, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		t.Fatal("baseline must be a regular JSON file no larger than 1MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var baseline chatResourceBaselineDocument
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&baseline); err != nil {
		t.Fatal(err)
	}
	if baseline.Version != 2 || baseline.InstrumentSHA256 != chatResourceInstrumentSHA256(t) {
		t.Fatal("baseline version or instrument SHA-256 differs")
	}
	group := func(rows []chatResourceResult) map[string][]int64 {
		t.Helper()
		if len(rows) != 18 {
			t.Fatal("performance gate requires all 18 workload rows")
		}
		seen := make(map[string]bool, 18)
		groups := make(map[string][]int64)
		for _, row := range rows {
			key := fmt.Sprintf("%s/%d/%d", row.Phase, row.Chats, row.Round)
			if seen[key] || row.TraceEnabled || row.DBMetadataEnabled || row.OwnersEnabled || row.OwnerRecords != nil || row.StatementCounts != nil || row.WALAfterFlush != nil || row.WALAfterDone != nil || row.SyncFlush != *chatResourceSyncFlush || row.Phase != "completed" && row.Phase != "canceled_shutdown" || row.Chats != 16 && row.Chats != 32 && row.Chats != 64 || row.Round < 1 || row.Round > 3 {
				t.Fatalf("invalid or duplicate baseline/current row: %s", key)
			}
			seen[key] = true
			if row.DBSettings.JournalMode != "wal" || row.DBSettings.Synchronous != 2 || row.DBSettings.ForeignKeys != 1 || row.DBSettings.PoolMaxOpen != 1 || row.DBSettings.PageSize <= 0 || row.DBSettings.WALAutoCheckpoint <= 0 {
				t.Fatalf("baseline/current SQLite safety configuration is incomplete or changed: %s", key)
			}
			if row.ModelCalls != int64(row.Chats) || row.MaxInflight != int64(row.Chats) || row.ActiveModels != 0 || row.PendingWork != 0 || row.Leases != 0 || row.PoolInUse != 0 || row.Before.FDs != row.AfterCleanup.FDs || len(row.AdmissionNS) != row.Chats || len(row.IncrementalNS) != row.Chats || len(row.FlushWaitNS) != row.Chats || len(row.FlushPhases) != row.Chats {
				t.Fatalf("baseline/current behavior or timing coverage did not pass: %s", key)
			}
			if row.Phase == "completed" && (row.DoneFrames != row.Chats || len(row.FinalNS) != row.Chats || len(row.FinalWaitNS) != row.Chats || len(row.FinalPhases) != row.Chats || row.CanceledTurns != 0 || row.Interrupted != 0) || row.Phase == "canceled_shutdown" && (row.DoneFrames != 0 || len(row.FinalNS) != 0 || len(row.FinalWaitNS) != 0 || len(row.FinalPhases) != 0 || row.CanceledTurns != 1 || row.Interrupted != row.Chats-1) {
				t.Fatalf("baseline/current terminal behavior did not pass: %s", key)
			}
			for _, samples := range []struct {
				phases       []chatResourcePhases
				total, begin []int64
			}{
				{row.FlushPhases, row.IncrementalNS, row.FlushWaitNS}, {row.FinalPhases, row.FinalNS, row.FinalWaitNS},
			} {
				for index, phase := range samples.phases {
					if phase.OwnerID != 0 || phase.WALBefore != nil || phase.WALAfter != nil || phase.TotalNS != samples.total[index] || phase.BeginNS != samples.begin[index] || phase.BeginNS < 0 || phase.StatementsNS < 0 || phase.RowsNS < 0 || phase.CommitNS <= 0 || phase.OtherNS < 0 || phase.Statements <= 0 || phase.TotalNS != phase.BeginNS+phase.StatementsNS+phase.RowsNS+phase.CommitNS+phase.OtherNS {
						t.Fatalf("invalid transaction phase accounting: %s/%d", key, index)
					}
				}
			}
			for name, values := range map[string][]int64{"admission": row.AdmissionNS, "incremental_flush": row.IncrementalNS, "final_save": row.FinalNS} {
				if len(values) == 0 {
					continue // canceled轮次不执行成功整轮保存。
				}
				for index, value := range values {
					if value <= 0 {
						t.Fatalf("non-positive timing sample: %s/%s/%d", key, name, index)
					}
				}
				metric := fmt.Sprintf("%s/%d/%s", row.Phase, row.Chats, name)
				groups[metric] = append(groups[metric], values...)
			}
		}
		return groups
	}
	reference, observed := group(baseline.Rows), group(current)
	keys := make([]string, 0, len(reference))
	for key := range reference {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	p95 := func(values []int64) int64 {
		values = slices.Clone(values)
		slices.Sort(values)
		return values[(len(values)*95+99)/100-1] // nearest-rank，固定汇总三轮所有样本。
	}
	for _, key := range keys {
		before, after := p95(reference[key]), p95(observed[key])
		passed := float64(after) <= float64(before)*1.20
		t.Logf("CHAT_P95_GATE %s baseline_ns=%d current_ns=%d change=%.2f%% pass=%t", key, before, after, (float64(after)/float64(before)-1)*100, passed)
		if !passed {
			t.Errorf("%s p95 exceeds the unchanged 20%% regression gate", key)
		}
	}
}
