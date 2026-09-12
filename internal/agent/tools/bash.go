package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/lyonmu/kaguya/internal/global"
)

type BashInput struct {
	Command string   `json:"command" description:"Shell command string executed by bash -c from the project root. Quote paths with spaces. No command array or separate cwd parameter."`
	Timeout *float64 `json:"timeout,omitempty" description:"Positive timeout in seconds, not milliseconds (maximum 2147483.647). Omit to use the system timeout. A larger value cannot extend the system timeout; the smaller limit wins."`
}

var newline = []byte{'\n'}

func (s *Set) BashTool() fantasy.AgentTool {
	return tool(s, "bash", `Run shell commands for targeted searches, directory listings, builds and tests. Each call starts in the project root; cd and environment changes do not persist to later calls. Use command, not cmd; timeout is in seconds, not milliseconds. For a subdirectory, put cd in the command. Returns stdout/stderr and exit status; only the last 2000 lines or 50KB are shown, with a temporary output path on truncation. Use read on that path instead of rerunning just to see output. Example: {"command":"rg -n 'main' src","timeout":30}. Runs with the service user's permissions, not in a sandbox; cancellation stops the process group but does not undo side effects.`, s.bash)
}

// outputAccumulator 收集命令输出：内存保留 tail，超限时写入会话输出目录。
type outputAccumulator struct {
	mu                        sync.Mutex
	s                         *Set
	tail                      []byte
	totalBytes, totalNewlines int
	lastNewline               bool
	file                      *os.File
	fileBytes                 int64
	path                      string
	quotaHit                  bool
	onQuota                   func() // 达到磁盘配额时终止命令
	err                       error
}

// conversationQuotaHit 报告会话输出目录是否已达到磁盘上限。
// 只统计本应用创建的文件，不跟随符号链接。
func (o *outputAccumulator) conversationQuotaHit() bool {
	dir := filepath.Dir(filepath.Join(o.s.tempBase, o.path))
	var total int64
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total >= maxConversationOutputBytes
}

func (o *outputAccumulator) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return 0, o.err
	}
	o.totalBytes += len(p)
	// bytes.Count 不复制输出切片；命令可能产生数十 MB 输出。
	o.totalNewlines += bytes.Count(p, newline)
	if len(p) > 0 {
		o.lastNewline = p[len(p)-1] == '\n'
	}
	lines := o.totalNewlines
	if !o.lastNewline && o.totalBytes > 0 {
		lines++
	}
	if o.file == nil && (o.totalBytes > MaxBytes || lines > MaxLines) {
		if err := o.s.tempRoot.MkdirAll(filepath.Dir(filepath.Join(o.s.conversationOutputDir(), "x")), 0700); err != nil {
			o.err = err
			return 0, err
		}
		if global.Id == nil {
			o.err = errors.New("global ID generator is not initialized")
			return 0, o.err
		}
		id, err := global.Id.GenID()
		if err != nil {
			o.err = fmt.Errorf("generate command output ID: %w", err)
			return 0, o.err
		}
		o.path = filepath.Join(o.s.conversationOutputDir(), fmt.Sprintf("bash-%d.log", id))
		if o.conversationQuotaHit() {
			// 不静默丢弃，也不新建超限文件；已累计的内存 tail 仍可返回。
			o.quotaHit = true
			o.path = ""
			if o.onQuota != nil {
				o.onQuota()
			}
			return len(p), nil
		}
		f, err := o.s.tempRoot.OpenFile(o.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			o.err = err
			return 0, err
		}
		o.file = f
		if _, err = f.Write(o.tail); err != nil {
			o.err = err
			return 0, err
		}
		o.fileBytes = int64(len(o.tail))
	}
	if o.file != nil {
		// 单文件配额：写满后停止落盘并终止命令，tail 与原因如实返回。
		limit := min(o.s.commandOutputLimit, maxCommandOutputBytes)
		if remain := limit - o.fileBytes; remain > 0 {
			chunk := p
			if int64(len(chunk)) > remain {
				chunk = chunk[:remain]
			}
			if _, err := o.file.Write(chunk); err != nil {
				o.err = err
				return 0, err
			}
			o.fileBytes += int64(len(chunk))
		}
		if o.fileBytes >= limit {
			o.quotaHit = true
			if o.onQuota != nil {
				o.onQuota()
			}
		}
	}
	o.tail = append(o.tail, p...)
	// Retain twice the output byte limit to always have enough complete tail lines.
	if len(o.tail) > MaxBytes*2 {
		o.tail = append([]byte(nil), o.tail[len(o.tail)-MaxBytes*2:]...)
	}
	return len(p), nil
}
func (o *outputAccumulator) finish() (fantasy.ToolResponse, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.file != nil {
		if err := o.file.Close(); err != nil && o.err == nil {
			o.err = err
		}
	}
	if o.err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("save command output: %w", o.err)
	}
	data := o.tail
	for len(data) > 0 && !utf8.RuneStart(data[0]) {
		data = data[1:]
	}
	r := truncate(strings.ToValidUTF8(string(data), "�"), MaxLines, true)
	r.TotalBytes = o.totalBytes
	r.TotalLines = o.totalNewlines
	if !o.lastNewline && o.totalBytes > 0 {
		r.TotalLines++
	}
	r.Truncated = o.path != ""
	text := r.Content
	if text == "" {
		text = "(no output)"
	}
	details := map[string]any{}
	if r.Truncated {
		fullOutputPath := filepath.Join(o.s.tempBase, o.path)
		text += fmt.Sprintf("\n\n[Showing last %d lines / %d bytes of %d lines / %d bytes. Full output: %s]", r.OutputLines, r.OutputBytes, r.TotalLines, r.TotalBytes, fullOutputPath)
		details["truncation"] = r
		details["fullOutputPath"] = fullOutputPath
	}
	if o.quotaHit {
		// 配额原因与可用路径必须明确告知，而不是让模型误以为命令正常结束。
		limit := min(o.s.commandOutputLimit, maxCommandOutputBytes)
		text += fmt.Sprintf("\n\n[Output limit reached (%d MiB per command or %d MiB per conversation). The command was stopped and later output was not saved.]", limit>>20, maxConversationOutputBytes>>20)
		details["outputQuotaBytes"] = limit
	}
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(text), details), nil
}
func (s *Set) bash(ctx context.Context, in BashInput) (fantasy.ToolResponse, error) {
	if strings.TrimSpace(in.Command) == "" {
		return fantasy.ToolResponse{}, errors.New("command is required")
	}
	limit := s.commandTimeout
	if limit <= 0 {
		limit = 120 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	if in.Timeout != nil {
		if math.IsNaN(*in.Timeout) || math.IsInf(*in.Timeout, 0) || *in.Timeout <= 0 || *in.Timeout > 2147483.647 {
			return fantasy.ToolResponse{}, errors.New("timeout must be positive and at most 2147483.647 seconds")
		}
		var stop context.CancelFunc
		runCtx, stop = context.WithTimeout(runCtx, time.Duration(*in.Timeout*float64(time.Second)))
		defer stop()
	}
	cmd := exec.CommandContext(runCtx, "bash", "-c", in.Command)
	cmd.Dir = s.cwd
	cmd.Env = append(os.Environ(), "KAGUYA_WORKSPACE="+s.cwd)
	output := &outputAccumulator{s: s, onQuota: cancel}
	cmd.Stdout = output
	cmd.Stderr = output
	configureProcess(cmd)
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		_ = cmd.Cancel()
	}
	response, outputErr := output.finish()
	if outputErr != nil {
		return fantasy.ToolResponse{}, outputErr
	}
	if ctx.Err() != nil {
		return response, ctx.Err()
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		seconds := limit.Seconds()
		if in.Timeout != nil {
			seconds = min(seconds, *in.Timeout)
		}
		response.Content += fmt.Sprintf("\n\nCommand timed out after %g seconds", seconds)
		response.IsError = true
		return response, nil
	}
	if err != nil {
		response.IsError = true
		response.Content += "\n\n" + err.Error()
	}
	return response, nil
}
