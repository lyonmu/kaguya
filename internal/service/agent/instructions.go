package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/lyonmu/kaguya/internal/agent/instructions"
	"github.com/lyonmu/kaguya/internal/db"
	"github.com/lyonmu/kaguya/internal/ent"
	"github.com/lyonmu/kaguya/internal/ent/kaguyaconversation"
)

// instructionBundle 是一次轮次使用的指令文本与冻结/归档信息。
// split 模式全局每轮动态、项目首个成功轮次冻结；legacy 保留旧合并快照不拆解。
type instructionBundle struct {
	text            string
	format          string
	projectSnapshot *string // 非 nil 时需要在本轮成功后冻结
	globalHash      string
	globalVersion   int64
	legacy          bool
}

// DefaultGlobalInstructions 是 appRuntime 持有的全局指令 Manager；存在时
// 每轮使用其当前快照，文件变更从下一轮生效。未启动 runtime 的测试回退到直接读取。
var DefaultGlobalInstructions *instructions.Manager

// The persisted snapshot survives restarts and compaction. split 模式每轮读取动态
// 全局版本与冻结项目快照；legacy 旧合并快照不拆解、不静默重读项目文件。
func conversationInstructions(ctx context.Context, id string, paths []string, projectDir string) (instructionBundle, error) {
	row, err := db.EntClient.KaguyaConversation.Get(ctx, id)
	if err != nil && !ent.IsNotFound(err) {
		return instructionBundle{}, err
	}
	if row != nil {
		if row.DeletedAt != nil {
			return instructionBundle{}, ErrConversationNotFound
		}
		if row.InstructionsFormat == kaguyaconversation.InstructionsFormatLegacy {
			switch {
			case row.AgentInstructions == nil:
				// 从未冻结：按 split 初始化，本轮成功后写入项目快照。
				return splitInstructionBundle(paths, projectDir, nil)
			case *row.AgentInstructions == "":
				// 旧快照明确为空：保持空项目快照，不重新发现项目指令。
				empty := ""
				return splitInstructionBundle(paths, "", &empty)
			case row.ProjectID == nil:
				// 旧普通会话：忽略合并文本，使用动态全局与空项目快照。
				return splitInstructionBundle(paths, "", nil)
			default:
				// 旧项目非空合并快照：保留 opaque 文本，UI 提供显式升级。
				hash, version := globalInstructionMeta()
				return instructionBundle{
					text: *row.AgentInstructions, format: string(kaguyaconversation.InstructionsFormatLegacy),
					globalHash: hash, globalVersion: version, legacy: true,
				}, nil
			}
		}
		return splitInstructionBundle(paths, projectDir, row.ProjectInstructions)
	}
	return splitInstructionBundle(paths, projectDir, nil)
}

// splitInstructionBundle 组装动态全局与项目指令；frozen 非 nil 表示项目已冻结。
func splitInstructionBundle(paths []string, projectDir string, frozen *string) (instructionBundle, error) {
	global, err := globalInstructions(paths)
	if err != nil {
		return instructionBundle{}, err
	}
	hash, version := globalInstructionMeta()
	var project string
	var snapshot *string
	if frozen != nil {
		project = *frozen
	} else {
		project, err = loadProjectInstructions(projectDir)
		if err != nil {
			return instructionBundle{}, err
		}
		snapshot = &project
	}
	return instructionBundle{
		text: global + project, format: string(kaguyaconversation.InstructionsFormatSplit),
		projectSnapshot: snapshot, globalHash: hash, globalVersion: version,
	}, nil
}

// globalInstructionMeta 返回当前动态全局版本的哈希与进程内版本；无 Manager 时为空。
func globalInstructionMeta() (string, int64) {
	if manager := DefaultGlobalInstructions; manager != nil {
		status := manager.Status()
		return status.Hash, status.Version
	}
	return "", 0
}

// Global files are explicitly trusted paths outside the project root.
// 运行时有 Manager 时使用其当前快照；否则用同一加载器直接读取（无数量/大小上限）。
func globalInstructions(paths []string) (string, error) {
	if manager := DefaultGlobalInstructions; manager != nil {
		return manager.Snapshot().Text, nil
	}
	snapshot, _, err := instructions.Load(paths)
	if err != nil {
		return "", err
	}
	return snapshot.Text, nil
}

// loadProjectInstructions 读取项目根目录的 AGENTS.md 快照，受 256 KiB 预算与
// os.Root 边界限制；全局指令不参与该预算。
func loadProjectInstructions(projectDir string) (string, error) {
	if projectDir == "" {
		return "", nil
	}
	var out strings.Builder
	root, err := os.OpenRoot(projectDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return "", err
	}
	entries, err := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	reader := instructionReader{}
	for _, name := range instructionNames(entries) {
		f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return "", fmt.Errorf("open project instructions %q: %w", name, err)
		}
		if err := reader.append(f, "Project", filepath.Join(projectDir, name)); err != nil {
			return "", err
		}
	}
	out.WriteString(reader.out.String())
	return out.String(), nil
}

func loadInstructions(paths []string, projectDir string) (string, error) {
	reader := instructionReader{}
	for _, path := range paths {
		if strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			path = filepath.Join(home, path[2:])
		}
		candidates := []string{path}
		if strings.EqualFold(filepath.Base(path), "AGENTS.md") {
			entries, err := os.ReadDir(filepath.Dir(path))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return "", fmt.Errorf("list global instructions %q: %w", path, err)
			}
			candidates = nil
			// ReadDir sorts names; if differently cased files coexist, include all
			// distinct files deterministically, without rewriting the filesystem.
			for _, entry := range entries {
				if strings.EqualFold(entry.Name(), "AGENTS.md") {
					candidates = append(candidates, filepath.Join(filepath.Dir(path), entry.Name()))
				}
			}
		}
		for _, candidate := range candidates {
			f, err := os.OpenFile(candidate, os.O_RDONLY|syscall.O_NONBLOCK, 0)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return "", fmt.Errorf("open global instructions %q: %w", candidate, err)
			}
			if err := reader.append(f, "Global", candidate); err != nil {
				return "", err
			}
		}
	}
	if projectDir != "" {
		// Read through os.Root so project instruction symlinks cannot escape the
		// database-owned workspace, including at the actual open operation.
		root, err := os.OpenRoot(projectDir)
		if err != nil {
			return "", err
		}
		defer root.Close()
		dir, err := root.Open(".")
		if err != nil {
			return "", err
		}
		entries, err := dir.ReadDir(-1)
		closeErr := dir.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		names := instructionNames(entries)
		for _, name := range names {
			f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				return "", fmt.Errorf("open project instructions %q: %w", name, err)
			}
			if err := reader.append(f, "Project", filepath.Join(projectDir, name)); err != nil {
				return "", err
			}
		}
	}
	return reader.out.String(), nil
}

type instructionReader struct {
	out  strings.Builder
	seen []os.FileInfo
}

func instructionNames(entries []os.DirEntry) []string {
	var names []string
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), "AGENTS.md") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func (r *instructionReader) append(f *os.File, scope, path string) (err error) {
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	const maxBytes = 256 * 1024
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("instructions %q must be a regular file", path)
	}
	for _, previous := range r.seen {
		if os.SameFile(previous, info) {
			return nil
		}
	}
	r.seen = append(r.seen, info)
	data, err := io.ReadAll(io.LimitReader(f, int64(maxBytes-r.out.Len()+1)))
	if err != nil {
		return err
	}
	if len(data)+r.out.Len() > maxBytes {
		return fmt.Errorf("instructions exceed %d bytes", maxBytes)
	}
	if !utf8.Valid(data) {
		return fmt.Errorf("instructions %q must be UTF-8", path)
	}
	if strings.TrimSpace(string(data)) != "" {
		fmt.Fprintf(&r.out, "\n\n%s instructions from %s:\n%s", scope, path, data)
		if r.out.Len() > maxBytes {
			return fmt.Errorf("instructions exceed %d bytes", maxBytes)
		}
	}
	return nil
}
