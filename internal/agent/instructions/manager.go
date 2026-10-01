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
	"sort"
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
	Version    int64     `json:"version"`
	Hash       string    `json:"hash,omitempty"`
	LoadedAt   time.Time `json:"loaded_at,omitempty"`
	Generation int64     `json:"generation"`
	LastError  string    `json:"last_error,omitempty"`
}

// Manager 持有全局路径配置、当前快照与单个 fsnotify watcher。
type Manager struct {
	logger *zap.Logger

	mu         sync.Mutex
	paths      []string
	generation int64
	lastError  string
	watches    map[string]struct{}
	watcher    *fsnotify.Watcher

	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}
	closed bool

	snapshot atomic.Pointer[Snapshot]

	// 窄测试缝：更短的去抖/验证/对账间隔。
	debounce  time.Duration
	settle    time.Duration
	reconcile time.Duration

	reloadMu sync.Mutex
	pending  bool
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
	snapshot, watches, err := Load(paths)
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
	if err := manager.applyWatches(watches); err != nil {
		_ = watcher.Close()
		cancel()
		return nil, err
	}
	return manager, nil
}

// Start 启动事件循环；结束时 Done 关闭。
func (m *Manager) Start() {
	go m.loop()
	go m.loadLoop()
}

// Done 在 watcher 与读取 worker 完全退出后关闭。
func (m *Manager) Done() <-chan struct{} { return m.done }

// Close 停止监听并等待 worker 退出；重复调用安全。
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.mu.Unlock()
	m.cancel()
}

// Snapshot 返回当前不可变快照；即使全部路径缺失也会返回非 nil。
func (m *Manager) Snapshot() *Snapshot {
	if snapshot := m.snapshot.Load(); snapshot != nil {
		return snapshot
	}
	return &Snapshot{}
}

// Status 返回不含正文的运行状态。
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := m.Snapshot()
	return Status{
		Version: snapshot.Version, Hash: snapshot.Hash, LoadedAt: snapshot.LoadedAt,
		Generation: m.generation, LastError: m.lastError,
	}
}

// Reconfigure 在数据库提交路径列表后应用新配置：先完整加载候选并计算 watch 集合，
// 成功才原子交换文本与 watches；失败保留旧版本并由后台重试。
func (m *Manager) Reconfigure(paths []string) error {
	m.mu.Lock()
	m.generation++
	generation := m.generation
	m.paths = append([]string(nil), paths...)
	m.mu.Unlock()

	snapshot, watches, err := Load(paths)
	if err != nil {
		m.setError(err)
		m.logger.Warn("reload global instructions after configuration failed; keeping previous version",
			zap.Int64("generation", generation), zap.String("reason", err.Error()))
		m.scheduleReload()
		return err
	}
	if err := m.applyWatches(watches); err != nil {
		m.setError(err)
		m.scheduleReload()
		return err
	}
	m.store(snapshot)
	m.clearError()
	// 后台再验证一次稳定性，配置提交本身不等待静默期。
	m.scheduleReload()
	return nil
}

// applyWatches 让 watcher 与目标目录集合一致：新增缺失目录，移除不再需要的目录。
func (m *Manager) applyWatches(target map[string]struct{}) error {
	m.mu.Lock()
	current := make(map[string]struct{}, len(m.watches))
	for dir := range m.watches {
		current[dir] = struct{}{}
	}
	m.mu.Unlock()
	for dir := range target {
		if _, ok := current[dir]; ok {
			continue
		}
		if err := m.watcher.Add(dir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("watch instructions directory %q: %w", dir, err)
		}
		current[dir] = struct{}{}
	}
	for dir := range current {
		if _, ok := target[dir]; ok {
			continue
		}
		_ = m.watcher.Remove(dir)
		delete(current, dir)
	}
	m.mu.Lock()
	m.watches = current
	m.mu.Unlock()
	return nil
}

func (m *Manager) setError(err error) {
	m.mu.Lock()
	if err != nil {
		m.lastError = err.Error()
	}
	m.mu.Unlock()
}

func (m *Manager) clearError() {
	m.mu.Lock()
	m.lastError = ""
	m.mu.Unlock()
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
	defer close(m.done)
	ticker := time.NewTicker(m.reconcile)
	defer ticker.Stop()
	var debounce *time.Timer
	var debounceC <-chan time.Time
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
		_ = m.watcher.Close()
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
			m.reconcileWatches()
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

// reconcileWatches 修复被 OS 丢弃的 watch（目录删除后重建、overflow 等），
// 只检查配置相关目录，不递归扫描 home。
func (m *Manager) reconcileWatches() {
	m.mu.Lock()
	paths := append([]string(nil), m.paths...)
	m.mu.Unlock()
	// 只修复配置相关目录的 watch；内容差异由文件事件或本次重扫处理。
	_ = m.applyWatches(WatchDirs(paths))
	m.scheduleReload()
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
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	m.mu.Lock()
	generation := m.generation
	paths := append([]string(nil), m.paths...)
	m.mu.Unlock()

	first, _, err := Load(paths)
	if err != nil {
		// remove/rename 原文件时保留上次有效版本；recreate 后恢复。
		m.setError(err)
		m.logger.Warn("reload global instructions failed; keeping previous version", zap.String("reason", err.Error()))
		return
	}
	if !sleepContext(m.ctx, m.settle) {
		return
	}
	second, watches, err := Load(paths)
	if err != nil {
		m.setError(err)
		return
	}
	if first.Hash != second.Hash {
		// 两次读取不一致：丢弃候选并重新调度。
		m.scheduleReload()
		return
	}
	m.mu.Lock()
	stale := m.generation != generation
	m.mu.Unlock()
	if stale {
		// 落后 generation 的结果不能覆盖新配置。
		return
	}
	// 运行中此前存在的文件消失按 transient error 处理：保留旧版本等待 recreate，
	// 而不是立刻清空指令；文件重新出现后下一次 reload 恢复。
	if missing, ok := disappearedSource(m.Snapshot(), second); ok {
		m.setError(fmt.Errorf("instructions %q disappeared; keeping previous version", missing))
		return
	}
	if err := m.applyWatches(watches); err != nil {
		m.setError(err)
		return
	}
	m.store(second)
	m.clearError()
}

// disappearedSource 报告上一版本存在、新候选缺失且当前确实不存在的文件。
func disappearedSource(previous, next *Snapshot) (string, bool) {
	nextPaths := make(map[string]struct{}, len(next.Sources))
	for _, source := range next.Sources {
		nextPaths[source.Path] = struct{}{}
	}
	for _, source := range previous.Sources {
		if _, ok := nextPaths[source.Path]; ok {
			continue
		}
		if _, err := os.Stat(source.Path); os.IsNotExist(err) {
			return source.Path, true
		}
	}
	return "", false
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
func Load(paths []string) (*Snapshot, map[string]struct{}, error) {
	sources := make([]Source, 0, len(paths))
	seen := make([]os.FileInfo, 0, len(paths))
	var text strings.Builder
	for _, configured := range paths {
		path, err := expandHome(configured)
		if err != nil {
			return nil, nil, err
		}
		candidates := []string{path}
		if strings.EqualFold(filepath.Base(path), "AGENTS.md") {
			entries, err := os.ReadDir(filepath.Dir(path))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, nil, fmt.Errorf("list global instructions %q: %w", path, err)
			}
			candidates = nil
			for _, entry := range entries {
				if strings.EqualFold(entry.Name(), "AGENTS.md") {
					candidates = append(candidates, filepath.Join(filepath.Dir(path), entry.Name()))
				}
			}
			sort.Strings(candidates)
		}
		for _, candidate := range candidates {
			file, info, err := openRegular(candidate)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			data, readErr := readAll(file)
			file.Close()
			if readErr != nil {
				return nil, nil, fmt.Errorf("read global instructions %q: %w", candidate, readErr)
			}
			if !utf8.Valid(data) {
				return nil, nil, fmt.Errorf("instructions %q must be UTF-8", candidate)
			}
			duplicate := false
			for _, previous := range seen {
				if os.SameFile(previous, info) {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			seen = append(seen, info)
			content := string(data)
			hash := sha256.Sum256(data)
			sources = append(sources, Source{Path: candidate, Text: content, Hash: hex.EncodeToString(hash[:8])})
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
