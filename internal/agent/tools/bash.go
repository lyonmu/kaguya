package tools

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"charm.land/fantasy"
)

type BashInput struct {
	Command string   `json:"command" description:"Bash command to run in the project directory."`
	Timeout *float64 `json:"timeout,omitempty" description:"Optional positive timeout in seconds. No default timeout; cancellation always stops the process group."`
}

func (s *Set) BashTool() fantasy.AgentTool {
	return tool(s, "bash", "Execute a bash command in the project directory. Returns stdout and stderr; keeps the last 2000 lines or 50KB. Truncated output is saved in .kaguya/tool-output and can be read with read. Optional timeout in seconds. This executes with the server user's permissions and is NOT a sandbox.", s.bash)
}

type outputAccumulator struct {
	mu                        sync.Mutex
	s                         *Set
	tail                      []byte
	totalBytes, totalNewlines int
	lastNewline               bool
	file                      *os.File
	path                      string
	err                       error
}

func (o *outputAccumulator) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return 0, o.err
	}
	o.totalBytes += len(p)
	o.totalNewlines += strings.Count(string(p), "\n")
	if len(p) > 0 {
		o.lastNewline = p[len(p)-1] == '\n'
	}
	lines := o.totalNewlines
	if !o.lastNewline && o.totalBytes > 0 {
		lines++
	}
	if o.file == nil && (o.totalBytes > MaxBytes || lines > MaxLines) {
		dir := ".kaguya/tool-output"
		if err := o.s.root.MkdirAll(dir, 0700); err != nil {
			o.err = err
			return 0, err
		}
		o.path = filepath.Join(dir, "bash-"+rand.Text()+".log")
		f, err := o.s.root.OpenFile(o.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			o.err = err
			return 0, err
		}
		o.file = f
		if _, err = f.Write(o.tail); err != nil {
			o.err = err
			return 0, err
		}
	}
	if o.file != nil {
		if _, err := o.file.Write(p); err != nil {
			o.err = err
			return 0, err
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
		text += fmt.Sprintf("\n\n[Showing last %d lines / %d bytes of %d lines / %d bytes. Full output: %s]", r.OutputLines, r.OutputBytes, r.TotalLines, r.TotalBytes, o.path)
		details["truncation"] = r
		details["fullOutputPath"] = filepath.Join(o.s.cwd, o.path)
	}
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(text), details), nil
}
func (s *Set) bash(ctx context.Context, in BashInput) (fantasy.ToolResponse, error) {
	if strings.TrimSpace(in.Command) == "" {
		return fantasy.ToolResponse{}, errors.New("command is required")
	}
	runCtx, cancel := context.WithCancel(ctx)
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
	output := &outputAccumulator{s: s}
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
		response.Content += fmt.Sprintf("\n\nCommand timed out after %g seconds", *in.Timeout)
		response.IsError = true
		return response, nil
	}
	if err != nil {
		response.IsError = true
		response.Content += "\n\n" + err.Error()
	}
	return response, nil
}
