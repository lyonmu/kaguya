package project

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"

	dto "github.com/lyonmu/kaguya/internal/dto/project"
	"github.com/lyonmu/kaguya/internal/global"
)

const (
	gitTimeout     = 10 * time.Second
	gitOutputLimit = 4 << 20
	gitDiffLimit   = 1 << 20
	gitStderrLimit = 8 << 10
	gitFilesLimit  = 1000
)

var ErrNotGit = errors.New("project directory is not a git repository")

// limitedBuffer 限制命令输出占用的内存并记录截断，Write 始终成功以避免破坏管道。
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if remain := b.limit - b.buf.Len(); remain > 0 {
		if len(p) > remain {
			b.buf.Write(p[:remain])
			b.truncated = true
		} else {
			b.buf.Write(p)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

// runGit 只读执行 git：err 仅表示启动失败或超时，退出码交由调用方判断。
func runGit(ctx context.Context, dir string, limit int, args ...string) ([]byte, int, bool, error) {
	runCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "git", args...)
	cmd.Dir = dir
	stdout, stderr := &limitedBuffer{limit: limit}, &limitedBuffer{limit: gitStderrLimit}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if runCtx.Err() != nil {
		if ctx.Err() != nil {
			return nil, -1, false, ctx.Err()
		}
		return nil, -1, false, errors.New("git command timed out")
	}
	if err == nil {
		return stdout.buf.Bytes(), 0, stdout.truncated, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.buf.Bytes(), exitErr.ExitCode(), stdout.truncated, nil
	}
	return nil, -1, false, err
}

func gitUnavailable(err error) bool {
	var execErr *exec.Error
	return errors.As(err, &execErr) && errors.Is(execErr.Err, exec.ErrNotFound)
}

// gitRepository 判断目录是否位于 Git 工作树内，并返回项目根相对仓库根的前缀。
func gitRepository(ctx context.Context, dir string) (bool, string, error) {
	out, exitCode, _, err := runGit(ctx, dir, 4096, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false, "", err
	}
	if exitCode != 0 || strings.TrimSpace(string(out)) != "true" {
		return false, "", nil
	}
	prefixOut, exitCode, _, err := runGit(ctx, dir, 4096, "rev-parse", "--show-prefix")
	if err != nil {
		return false, "", err
	}
	if exitCode != 0 {
		return true, "", nil
	}
	return true, strings.TrimSpace(string(prefixOut)), nil
}

func trimGitPrefix(path, prefix string) (string, bool) {
	if prefix == "" {
		return path, true
	}
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	return path[len(prefix):], true
}

type gitEntry struct {
	xy   string
	path string
}

func (e gitEntry) status() string {
	if e.xy == "?" {
		return "untracked"
	}
	if e.xy == "UU" || e.xy == "AA" || e.xy == "DD" || e.xy[0] == 'U' {
		return "conflicted"
	}
	code := e.xy[1]
	if code == '.' {
		code = e.xy[0]
	}
	switch code {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	default:
		return "modified"
	}
}

func (e gitEntry) staged() bool { return e.xy != "?" && e.xy[0] != '.' && e.xy[1] == '.' }

// parsePorcelainV2 解析 `git status --porcelain=v2 -z --branch` 输出。
func parsePorcelainV2(data []byte) (string, string, []gitEntry) {
	var branch, head string
	entries := []gitEntry{}
	tokens := bytes.Split(data, []byte{0})
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if len(token) == 0 {
			continue
		}
		if token[0] == '#' {
			fields := bytes.SplitN(token, []byte{' '}, 3)
			if len(fields) == 3 {
				switch string(fields[1]) {
				case "branch.head":
					branch = string(fields[2])
				case "branch.oid":
					head = string(fields[2])
				}
			}
			continue
		}
		switch token[0] {
		case '1':
			fields := bytes.SplitN(token, []byte{' '}, 9)
			if len(fields) == 9 {
				entries = append(entries, gitEntry{xy: string(fields[1]), path: string(fields[8])})
			}
		case '2':
			fields := bytes.SplitN(token, []byte{' '}, 10)
			if len(fields) == 10 {
				// -z 模式下原路径是紧随其后的独立 token，前端只需要新路径。
				index++
				entries = append(entries, gitEntry{xy: string(fields[1]), path: string(fields[9])})
			}
		case 'u':
			fields := bytes.SplitN(token, []byte{' '}, 11)
			if len(fields) == 11 {
				entries = append(entries, gitEntry{xy: string(fields[1]), path: string(fields[10])})
			}
		case '?':
			if len(token) > 2 {
				entries = append(entries, gitEntry{xy: "?", path: string(token[2:])})
			}
		}
	}
	if strings.HasPrefix(head, "(") {
		head = ""
	}
	if len(head) > 7 {
		head = head[:7]
	}
	return branch, head, entries
}

// parseNumstat 解析 `git diff --numstat -z`，重命名取新路径；二进制文件没有数字统计。
func parseNumstat(data []byte) map[string][2]int {
	stats := map[string][2]int{}
	tokens := bytes.Split(data, []byte{0})
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if len(token) == 0 {
			continue
		}
		fields := bytes.SplitN(token, []byte{'\t'}, 3)
		if len(fields) != 3 {
			continue
		}
		path := string(fields[2])
		if path == "" {
			if index+2 >= len(tokens) {
				break
			}
			path = string(tokens[index+2])
			index += 2
		}
		added, addErr := strconv.Atoi(string(fields[0]))
		deleted, delErr := strconv.Atoi(string(fields[1]))
		if addErr != nil || delErr != nil {
			continue
		}
		stats[path] = [2]int{added, deleted}
	}
	return stats
}

// GitStatus 返回项目工作区的未提交变更；非 Git 目录返回 is_git=false 而不是错误。
func (s *ProjectSvc) GitStatus(ctx context.Context, id string) (*dto.GitStatusResp, error) {
	dir, err := s.Workspace(ctx, id)
	if err != nil {
		return nil, err
	}
	resp := &dto.GitStatusResp{Files: []dto.GitFile{}}
	inside, prefix, err := gitRepository(ctx, dir)
	if err != nil {
		if gitUnavailable(err) {
			resp.Message = "运行环境未找到 git 命令"
			return resp, nil
		}
		return nil, err
	}
	if !inside {
		resp.Message = "当前项目不是 Git 仓库"
		return resp, nil
	}
	out, exitCode, truncated, err := runGit(ctx, dir, gitOutputLimit, "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all", "--", ".")
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return nil, errors.New("git status failed")
	}
	resp.IsGit = true
	branch, head, entries := parsePorcelainV2(out)
	resp.Branch, resp.Head = branch, head
	resp.Truncated = truncated
	files := make([]dto.GitFile, 0, len(entries))
	for _, entry := range entries {
		path, ok := trimGitPrefix(entry.path, prefix)
		if !ok {
			continue
		}
		files = append(files, dto.GitFile{Path: path, Status: entry.status(), Staged: entry.staged()})
		if len(files) >= gitFilesLimit {
			resp.Truncated = true
			break
		}
	}
	// HEAD 尚不存在的空仓库没有可比较基线，行数统计保持为零。
	if stats, statExit, _, statErr := runGit(ctx, dir, gitOutputLimit, "diff", "--numstat", "-z", "HEAD", "--", "."); statErr == nil && statExit == 0 {
		numstat := parseNumstat(stats)
		for index := range files {
			if value, exists := numstat[prefix+files[index].Path]; exists {
				files[index].Additions, files[index].Deletions = value[0], value[1]
			}
		}
	}
	resp.Files = files
	global.Logger.Sugar().Debugf("git status: project=%s files=%d truncated=%t", id, len(files), resp.Truncated)
	return resp, nil
}

// GitDiff 返回单个文件相对 HEAD 的统一 diff；未跟踪文件按新增内容输出。
func (s *ProjectSvc) GitDiff(ctx context.Context, id, path string) (*dto.GitDiffResp, error) {
	dir, err := s.Workspace(ctx, id)
	if err != nil {
		return nil, err
	}
	rel, err := cleanRelative(path)
	if err != nil {
		return nil, err
	}
	inside, _, err := gitRepository(ctx, dir)
	if err != nil {
		if gitUnavailable(err) {
			return nil, ErrNotGit
		}
		return nil, err
	}
	if !inside {
		return nil, ErrNotGit
	}
	resp := &dto.GitDiffResp{Path: rel}
	_, headExit, _, err := runGit(ctx, dir, 4096, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return nil, err
	}
	_, trackExit, _, err := runGit(ctx, dir, 4096, "ls-files", "--error-unmatch", "--", rel)
	if err != nil {
		return nil, err
	}
	if headExit == 0 && trackExit == 0 {
		resp.Diff, resp.Truncated, err = gitDiffOutput(ctx, dir, "HEAD", rel)
	} else {
		// 空仓库和未跟踪文件都没有 HEAD 版本，与 /dev/null 对比得到全新增 diff。
		resp.Diff, resp.Truncated, err = gitDiffOutput(ctx, dir, "", rel)
	}
	if err != nil {
		return nil, err
	}
	resp.Binary = strings.Contains(resp.Diff, "Binary files") || strings.Contains(resp.Diff, "GIT binary patch")
	global.Logger.Sugar().Debugf("git diff: project=%s path=%s bytes=%d binary=%t truncated=%t", id, rel, len(resp.Diff), resp.Binary, resp.Truncated)
	return resp, nil
}

func gitDiffOutput(ctx context.Context, dir, revision, rel string) (string, bool, error) {
	args := []string{"-c", "core.quotePath=false", "diff", "--no-color", "--no-ext-diff"}
	if revision == "" {
		args = append(args, "--no-index", "--", "/dev/null", rel)
	} else {
		args = append(args, revision, "--", rel)
	}
	out, exitCode, truncated, err := runGit(ctx, dir, gitDiffLimit, args...)
	if err != nil {
		return "", false, err
	}
	// --no-index 退出码 1 表示存在差异，属于预期结果。
	if revision == "" && exitCode == 1 {
		return string(out), truncated, nil
	}
	if exitCode != 0 {
		return "", false, errors.New("git diff failed")
	}
	return string(out), truncated, nil
}
