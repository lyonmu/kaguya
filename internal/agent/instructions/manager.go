// Package instructions 加载并监听全局 Agent Markdown（AGENTS.md），以不可变
// 快照向聊天轮次提供“每轮固定版本”的全局指令。它只做全局显式路径：项目指令
// 仍由项目 os.Root 独立冻结，不经过本包。
//
// 文件一致性合同：写事件触发全量候选重载，读取前后校验 Stat 身份/大小/修改时间，
// 并用一次静默期后的重读交叉验证哈希；任一变化丢弃整个候选。两次稳定读取是
// 启发式检测，生产方仍需 temp+rename 才能保证严格一致版本。
package instructions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
)

// Source 是一个已加载的全局指令文件。
type Source struct {
	Path string
	Text string
	Hash string

	configured string
	info       os.FileInfo
}

// Snapshot 是不可变快照；Version 在同一 Manager 生命周期内单调递增，
// Hash 可在跨重启的轮次记录中比较。
type Snapshot struct {
	Version  int64
	Hash     string
	LoadedAt time.Time
	Text     string
	Sources  []Source
}

// Status 是供 SystemInfo 展示的只读状态；不包含正文。
type Status struct {
	Version          int64     `json:"version"`
	Hash             string    `json:"hash,omitempty"`
	LoadedAt         time.Time `json:"loaded_at,omitempty"`
	Generation       int64     `json:"generation"`
	LoadedGeneration int64     `json:"loaded_generation"`
	LastError        string    `json:"last_error,omitempty"`
}

// Manager 持有全局路径配置、当前快照与单个 fsnotify watcher。
type Manager struct {
	logger *zap.Logger

	mu               sync.Mutex
	paths            []string
	generation       int64
	loadedGeneration int64
	lastError        string
	watches          map[string]struct{}
	watcher          *fsnotify.Watcher

	ctx        context.Context
	cancel     context.CancelFunc
	wake       chan struct{}
	done       chan struct{}
	closed     bool
	started    bool
	workers    sync.WaitGroup
	finishOnce sync.Once

	snapshot atomic.Pointer[Snapshot]

	// 窄测试缝：更短的去抖/验证/对账间隔。
	debounce  time.Duration
	settle    time.Duration
	reconcile time.Duration

	// 仅用于确定性交错测试；Start 后不再修改。
	load     func(context.Context, []string) (*Snapshot, map[string]struct{}, error)
	addWatch func(string) error
}

// Options 允许测试缩短时间参数。
type Options struct {
	Debounce  time.Duration
	Settle    time.Duration
	Reconcile time.Duration
	Logger    *zap.Logger
}

const (
	defaultDebounce  = 200 * time.Millisecond
	defaultSettle    = 100 * time.Millisecond
	defaultReconcile = 2 * time.Second
)

// New 创建 Manager 并同步完成首次加载。缺失文件允许为空；非法 UTF-8、非普通
// 文件等严重错误会返回错误，由启动方中止进程。
func New(ctx context.Context, paths []string, options Options) (*Manager, error) {
	if options.Logger == nil {
		options.Logger = zap.NewNop()
	}
	if options.Debounce <= 0 {
		options.Debounce = defaultDebounce
	}
	if options.Settle <= 0 {
		options.Settle = defaultSettle
	}
	if options.Reconcile <= 0 {
		options.Reconcile = defaultReconcile
	}
	managerCtx, cancel := context.WithCancel(ctx)
	manager := &Manager{
		logger: options.Logger, ctx: managerCtx, cancel: cancel,
		wake: make(chan struct{}, 1), done: make(chan struct{}),
		debounce: options.Debounce, settle: options.Settle, reconcile: options.Reconcile,
		watches: map[string]struct{}{},
	}
	manager.load = Load
	snapshot, watches, err := manager.load(managerCtx, paths)
	if err != nil {
		cancel()
		return nil, err
	}
	manager.paths = append([]string(nil), paths...)
	manager.store(snapshot)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create instructions watcher: %w", err)
	}
	manager.watcher = watcher
	manager.addWatch = watcher.Add
	if err := manager.applyWatches(watches); err != nil {
		_ = watcher.Close()
		cancel()
		return nil, err
	}
	return manager, nil
}

// Start 幂等启动事件循环和唯一读取 worker；Done 等待二者及 watcher 全部退出。
func (m *Manager) Start() {
	m.mu.Lock()
	if m.started || m.closed {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.workers.Add(2)
	m.mu.Unlock()
	go func() { defer m.workers.Done(); m.loop() }()
	go func() { defer m.workers.Done(); m.loadLoop() }()
	go func() { m.workers.Wait(); m.finish() }()
}

func (m *Manager) Done() <-chan struct{} { return m.done }

// Close 发出停止信号；调用方通过 Done 等待读取完成。未 Start 也会释放 watcher。
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	started := m.started
	m.mu.Unlock()
	m.cancel()
	if !started {
		m.finish()
	}
}

func (m *Manager) finish() {
	m.finishOnce.Do(func() {
		_ = m.watcher.Close()
		close(m.done)
	})
}

// Snapshot 返回当前不可变快照；即使全部路径缺失也会返回非 nil。
func (m *Manager) Snapshot() *Snapshot {
	if snapshot := m.snapshot.Load(); snapshot != nil {
		copy := *snapshot
		copy.Sources = append([]Source(nil), snapshot.Sources...)
		return &copy
	}
	return &Snapshot{}
}

// Status 返回不含正文的运行状态。
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := m.snapshot.Load()
	return Status{
		Version: snapshot.Version, Hash: snapshot.Hash, LoadedAt: snapshot.LoadedAt,
		Generation: m.generation, LoadedGeneration: m.loadedGeneration, LastError: m.lastError,
	}
}

// Reconfigure 只接受已提交配置并通知 worker；文件读取与发布均在后台完成。
func (m *Manager) Reconfigure(paths []string) error {
	m.mu.Lock()
	if m.closed || m.ctx.Err() != nil {
		m.mu.Unlock()
		return ErrClosed
	}
	m.generation++
	m.lastError = ""
	m.paths = append([]string(nil), paths...)
	m.mu.Unlock()
	m.scheduleReload()
	return nil
}

var ErrClosed = errors.New("instruction manager is closed")

// applyWatches 让 watcher 与目标目录集合一致：新增缺失目录，移除不再需要的目录。
func (m *Manager) applyWatches(target map[string]struct{}) error {
	// OS 可在目录删除时自行撤销 watch；以真实账本修复遗漏。
	current := make(map[string]struct{})
	for _, dir := range m.watcher.WatchList() {
		current[dir] = struct{}{}
	}
	for dir := range target {
		if _, ok := current[dir]; ok {
			continue
		}
		if err := m.addWatch(dir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			// 已增加的 watch 必须纳入账本；失败时保留全部有效旧 watch。
			m.mu.Lock()
			m.watches = current
			m.mu.Unlock()
			return fmt.Errorf("watch instructions directory %q: %w", dir, err)
		}
		current[dir] = struct{}{}
	}
	for dir := range current {
		if _, ok := target[dir]; ok {
			continue
		}
		if err := m.watcher.Remove(dir); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
			m.mu.Lock()
			m.watches = current
			m.mu.Unlock()
			return err
		}
		delete(current, dir)
	}
	m.mu.Lock()
	m.watches = current
	m.mu.Unlock()
	return nil
}

// recordError 不公开底层文件错误，也不让旧 generation 覆盖新配置状态。
func (m *Manager) recordError(generation int64, reason string) {
	m.mu.Lock()
	current := !m.closed && m.ctx.Err() == nil && generation == m.generation
	changed := current && m.lastError != reason
	if current {
		m.lastError = reason
	}
	m.mu.Unlock()
	if changed {
		m.logger.Warn("reload global instructions failed; keeping previous version", zap.Int64("generation", generation), zap.String("reason", reason))
	}
}

// store 只在哈希变化时提升版本；内容相同不增加版本号。
func (m *Manager) store(snapshot *Snapshot) {
	previous := m.snapshot.Load()
	if previous != nil && previous.Hash == snapshot.Hash {
		// 内容未变：保留旧版本号，但更新加载时间与来源 path 列表。
		snapshot.Version = previous.Version
	}
	if previous == nil || previous.Hash != snapshot.Hash {
		snapshot.Version = 1
		if previous != nil {
			snapshot.Version = previous.Version + 1
		}
	}
	m.snapshot.Store(snapshot)
}

func (m *Manager) scheduleReload() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// loop 只消费事件与对账 ticker，不在事件循环内同步读大文件。
func (m *Manager) loop() {
	defer m.cancel()
	ticker := time.NewTicker(m.reconcile)
	defer ticker.Stop()
	var debounce *time.Timer
	var debounceC <-chan time.Time
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()
	resetDebounce := func() {
		if debounce == nil {
			debounce = time.NewTimer(m.debounce)
			debounceC = debounce.C
			return
		}
		if !debounce.Stop() {
			select {
			case <-debounce.C:
			default:
			}
		}
		debounce.Reset(m.debounce)
		debounceC = debounce.C
	}
	for {
		select {
		case <-m.ctx.Done():
			return
		case event, ok := <-m.watcher.Events:
			if !ok {
				return
			}
			if !m.relevantEvent(event) {
				continue
			}
			resetDebounce()
		case _, ok := <-m.watcher.Errors:
			if !ok {
				return
			}
			m.logger.Warn("instructions watcher reported an error; forcing rescan")
			resetDebounce()
		case <-debounceC:
			debounceC = nil
			m.scheduleReload()
		case <-ticker.C:
			m.scheduleReload()
		}
	}
}

// relevantEvent 只处理目标文件/目录相关事件；父目录监听会收到无关子项事件。
func (m *Manager) relevantEvent(event fsnotify.Event) bool {
	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename|fsnotify.Chmod) == 0 {
		return false
	}
	base := strings.ToLower(filepath.Base(event.Name))
	if base == "agents.md" {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, path := range m.paths {
		target := filepath.Base(path)
		if strings.EqualFold(target, "AGENTS.md") {
			target = "AGENTS.md"
		}
		if strings.EqualFold(base, target) {
			return true
		}
	}
	// 目录级事件（父目录被删除/重建）也触发一次全量检查。
	for dir := range m.watches {
		if event.Name == dir {
			return true
		}
	}
	return false
}

// loadLoop 是唯一的读取 worker：串行执行去抖后的“双重读取 + 哈希验证”。
func (m *Manager) loadLoop() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.wake:
		}
		// 合并短时间内的多次通知。
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(m.debounce):
		}
		m.reload()
	}
}

func (m *Manager) reload() {
	m.mu.Lock()
	generation := m.generation
	paths := append([]string(nil), m.paths...)
	m.mu.Unlock()

	if err := m.ctx.Err(); err != nil {
		return
	}
	if err := m.applyWatches(WatchDirs(paths)); err != nil {
		m.recordError(generation, "watch_failed")
		return
	}
	first, _, err := m.load(m.ctx, paths)
	if err != nil {
		m.recordError(generation, "read_failed")
		return
	}
	if !sleepContext(m.ctx, m.settle) {
		return
	}
	second, watches, err := m.load(m.ctx, paths)
	if err != nil {
		m.recordError(generation, "read_failed")
		return
	}
	if !sameSources(first, second) {
		m.recordError(generation, "source_unstable")
		m.scheduleReload()
		return
	}
	// 保留配置中的源临时消失时继续使用旧版；明确移除路径允许清空或替换。
	if disappearedSource(m.snapshot.Load(), second, paths) {
		m.recordError(generation, "source_missing")
		return
	}
	if err := m.ctx.Err(); err != nil {
		return
	}
	if err := m.applyWatches(watches); err != nil {
		m.recordError(generation, "watch_failed")
		return
	}
	// generation 判断与整份快照发布在同一个临界区内，不留旧结果覆盖的间隙。
	m.mu.Lock()
	if !m.closed && m.ctx.Err() == nil && m.generation == generation {
		m.store(second)
		m.loadedGeneration = generation
		m.lastError = ""
	} else if !m.closed && m.ctx.Err() == nil {
		m.scheduleReload()
	}
	m.mu.Unlock()
}

func sameSources(first, second *Snapshot) bool {
	if first.Hash != second.Hash || len(first.Sources) != len(second.Sources) {
		return false
	}
	for i, source := range first.Sources {
		other := second.Sources[i]
		if source.Path != other.Path || source.Hash != other.Hash || !sameFileVersion(source.info, other.info) {
			return false
		}
	}
	return true
}

func sameFileVersion(first, second os.FileInfo) bool {
	return first != nil && second != nil && os.SameFile(first, second) && first.Size() == second.Size() && first.ModTime().Equal(second.ModTime()) && first.Mode() == second.Mode()
}

func disappearedSource(previous, next *Snapshot, paths []string) bool {
	if previous == nil {
		return false
	}
	configured := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		configured[path] = struct{}{}
	}
	nextPaths := make(map[string]struct{}, len(next.Sources))
	for _, source := range next.Sources {
		nextPaths[source.Path] = struct{}{}
	}
	for _, source := range previous.Sources {
		if _, retained := configured[source.configured]; !retained {
			continue
		}
		if _, present := nextPaths[source.Path]; present {
			continue
		}
		if _, err := os.Stat(source.Path); os.IsNotExist(err) {
			return true
		}
	}
	return false
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Load 读取配置顺序的全局指令，返回不变快照与需要监听的目录集合。
// 缺失文件跳过；非普通文件、非法 UTF-8、不可读等返回错误。
func Load(ctx context.Context, paths []string) (*Snapshot, map[string]struct{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	sources := make([]Source, 0, len(paths))
	seen := make([]os.FileInfo, 0, len(paths))
	var text strings.Builder
	for _, configured := range paths {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		path, err := expandHome(configured)
		if err != nil {
			return nil, nil, err
		}
		candidates := []string{path}
		if strings.EqualFold(filepath.Base(path), "AGENTS.md") {
			candidates, err = instructionCandidates(ctx, path)
			if err != nil {
				return nil, nil, err
			}
		}
		for _, candidate := range candidates {
			file, info, err := openRegular(candidate)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			duplicate := false
			for _, previous := range seen {
				if os.SameFile(previous, info) {
					duplicate = true
					break
				}
			}
			if duplicate {
				_ = file.Close()
				continue
			}
			data, readErr := readAll(ctx, file)
			after, statErr := file.Stat()
			closeErr := file.Close()
			if readErr != nil {
				return nil, nil, fmt.Errorf("read global instructions %q: %w", candidate, readErr)
			}
			if statErr != nil {
				return nil, nil, statErr
			}
			if closeErr != nil {
				return nil, nil, closeErr
			}
			bound, err := os.Stat(candidate)
			if err != nil {
				return nil, nil, err
			}
			if !sameFileVersion(info, after) || !sameFileVersion(info, bound) {
				return nil, nil, fmt.Errorf("instructions %q changed while reading", candidate)
			}
			if !utf8.Valid(data) {
				return nil, nil, fmt.Errorf("instructions %q must be UTF-8", candidate)
			}
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			seen = append(seen, info)
			content := string(data)
			hash := sha256.Sum256(data)
			sources = append(sources, Source{Path: candidate, Text: content, Hash: hex.EncodeToString(hash[:8]), configured: configured, info: info})
			if strings.TrimSpace(content) != "" {
				fmt.Fprintf(&text, "\n\nGlobal instructions from %s:\n%s", candidate, content)
			}
		}
	}
	full := []byte(text.String())
	fullHash := sha256.Sum256(full)
	return &Snapshot{
		Hash: hex.EncodeToString(fullHash[:16]), LoadedAt: time.Now(), Text: text.String(), Sources: sources,
	}, WatchDirs(paths), nil
}

// WatchDirs 计算配置路径需要监听的目录：词法父目录与解析后的真实父目录，
// 父目录不存在时回退到最近存在的祖先。
func WatchDirs(paths []string) map[string]struct{} {
	watches := map[string]struct{}{}
	for _, configured := range paths {
		path, err := expandHome(configured)
		if err != nil {
			continue
		}
		if dir := nearestExistingDir(filepath.Dir(path)); dir != "" {
			watches[dir] = struct{}{}
		}
		// 符号链接可能指向其他目录：同时监听解析后的目标父目录，变更后重新解链。
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			if dir := nearestExistingDir(filepath.Dir(resolved)); dir != "" {
				watches[dir] = struct{}{}
			}
		}
	}
	return watches
}

func nearestExistingDir(dir string) string {
	for {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/")), nil
	}
	return path, nil
}
