package tools

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"charm.land/fantasy"
)

type LsInput struct {
	Path  string `json:"path,omitempty"`
	Limit *int   `json:"limit,omitempty" description:"Maximum entries; default 500."`
}
type FindInput struct {
	Pattern string `json:"pattern" description:"Glob pattern, e.g. *.go or src/**/*.ts."`
	Path    string `json:"path,omitempty"`
	Limit   *int   `json:"limit,omitempty" description:"Maximum results; default 1000."`
}
type GrepInput struct {
	Pattern    string `json:"pattern" description:"Regex or literal search pattern."`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	IgnoreCase bool   `json:"ignoreCase,omitempty"`
	Literal    bool   `json:"literal,omitempty"`
	Context    int    `json:"context,omitempty" description:"Lines before/after each match; default 0."`
	Limit      *int   `json:"limit,omitempty" description:"Maximum matches; default 100."`
}

func (s *Set) LsTool() fantasy.AgentTool {
	return tool(s, "ls", "List directory entries including dotfiles, sorted alphabetically with / for directories. Default 500 entries or 50KB.", s.ls)
}
func (s *Set) FindTool() fantasy.AgentTool {
	return tool(s, "find", "Find paths by glob using fd. Respects .gitignore and includes hidden files. Default 1000 results or 50KB. Requires fd on the host.", s.find)
}
func (s *Set) GrepTool() fantasy.AgentTool {
	return tool(s, "grep", "Search file contents using ripgrep. Returns relative paths and line numbers; respects .gitignore and includes hidden files. Default 100 matches or 50KB; matching lines are limited to 500 characters. Requires rg on the host.", s.grep)
}
func effectiveLimit(value *int, fallback int) (int, error) {
	if value == nil {
		return fallback, nil
	}
	if *value <= 0 || *value > 100000 {
		return 0, errors.New("limit must be between 1 and 100000")
	}
	return *value, nil
}
func (s *Set) searchPath(path string) (string, string, error) {
	if path == "" {
		path = "."
	}
	rel, err := s.resolve(path)
	if err != nil {
		return "", "", err
	}
	// Search processes do not follow directory links by default. Resolve the explicit
	// search root once, then validate it through the pinned workspace handle.
	abs, err := filepath.EvalSymlinks(filepath.Join(s.cwd, rel))
	if err != nil {
		return "", "", err
	}
	rel, err = s.resolve(abs)
	if err != nil {
		return "", "", err
	}
	if _, err := s.root.Stat(rel); err != nil {
		return "", "", err
	}
	return rel, abs, nil
}
func listResponse(lines []string, limit int, empty string) fantasy.ToolResponse {
	if len(lines) == 0 {
		return fantasy.NewTextResponse(empty)
	}
	more := len(lines) > limit
	if more {
		lines = lines[:limit]
	}
	r := truncate(strings.Join(lines, "\n"), 100000, false)
	text := r.Content
	if more {
		text += fmt.Sprintf("\n\n[%d results limit reached; increase limit or refine the search.]", limit)
	}
	if r.Truncated {
		text += "\n\n[50KB output limit reached; refine the search.]"
	}
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(text), map[string]any{"truncation": r, "limitReached": more})
}
func (s *Set) ls(ctx context.Context, in LsInput) (fantasy.ToolResponse, error) {
	limit, err := effectiveLimit(in.Limit, 500)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	path, _, err := s.searchPath(in.Path)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	info, err := s.root.Stat(path)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if !info.IsDir() {
		return fantasy.ToolResponse{}, errors.New("path must be a directory")
	}
	f, err := openReadFile(s.root, path)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := strings.ToLower(entries[i].Name()), strings.ToLower(entries[j].Name())
		if a == b {
			return entries[i].Name() < entries[j].Name()
		}
		return a < b
	})
	lines := []string{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return fantasy.ToolResponse{}, err
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
		if len(lines) > limit {
			break
		}
	}
	return listResponse(lines, limit, "(empty directory)"), nil
}

type cappedBuffer struct{ strings.Builder }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if remain := MaxBytes - b.Len(); remain > 0 {
		b.Builder.Write(p[:min(len(p), remain)])
	}
	return n, nil
}
func nulLines(data []byte, atEOF bool) (int, []byte, error) {
	for i, c := range data {
		if c == 0 {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// streamCommand bounds memory, distinguishes no matches from failure, and reaps
// the process on parse failure, cancellation or an intentional result limit.
func (s *Set) streamCommand(ctx context.Context, command string, args []string, nul bool, consume func(string) (bool, error)) error {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = s.cwd
	configureProcess(cmd)
	var stderr cappedBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("%s unavailable or failed to start: %w", command, err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), MaxFileBytes)
	if nul {
		scanner.Split(nulLines)
	}
	stopped := false
	var parseErr error
	for scanner.Scan() {
		stop, e := consume(scanner.Text())
		if e != nil || stop {
			stopped = true
			parseErr = e
			_ = cmd.Cancel()
			break
		}
	}
	if scanner.Err() != nil {
		parseErr = scanner.Err()
		_ = cmd.Cancel()
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if parseErr != nil {
		return parseErr
	}
	if stopped {
		return nil
	}
	if err != nil {
		var exit *exec.ExitError
		if command == "rg" && errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil
		}
		return fmt.Errorf("%s failed: %w: %s", command, err, stderr.String())
	}
	return nil
}
func (s *Set) find(ctx context.Context, in FindInput) (fantasy.ToolResponse, error) {
	limit, err := effectiveLimit(in.Limit, 1000)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if in.Pattern == "" {
		return fantasy.ToolResponse{}, errors.New("pattern is required")
	}
	_, path, err := s.searchPath(in.Path)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	args := []string{"--glob", "--color=never", "--hidden", "--print0", "--max-results", strconv.Itoa(limit + 1), "--exclude", ".kaguya/tool-output"}
	inRepo := false
	for p := path; ; p = filepath.Dir(p) {
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			inRepo = true
			break
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if !inRepo {
		args = append(args, "--no-require-git")
	}
	pattern := in.Pattern
	if strings.Contains(pattern, "/") {
		args = append(args, "--full-path")
		if !strings.HasPrefix(pattern, "/") && !strings.HasPrefix(pattern, "**/") {
			pattern = "**/" + pattern
		}
	}
	args = append(args, "--", pattern, path)
	lines := []string{}
	err = s.streamCommand(ctx, "fd", args, true, func(line string) (bool, error) {
		suffix := ""
		if strings.HasSuffix(line, "/") {
			suffix = "/"
		}
		rel, e := filepath.Rel(path, line)
		if e != nil {
			return false, e
		}
		lines = append(lines, filepath.ToSlash(rel)+suffix)
		return len(lines) > limit, nil
	})
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	return listResponse(lines, limit, "No files found matching pattern"), nil
}

type rgText struct {
	Text  *string `json:"text"`
	Bytes string  `json:"bytes"`
}

func (v rgText) value() (string, error) {
	if v.Text != nil {
		return *v.Text, nil
	}
	b, err := base64.StdEncoding.DecodeString(v.Bytes)
	return string(b), err
}
func (s *Set) grep(ctx context.Context, in GrepInput) (fantasy.ToolResponse, error) {
	limit, err := effectiveLimit(in.Limit, 100)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if in.Context < 0 || in.Context > 1000 {
		return fantasy.ToolResponse{}, errors.New("context must be between 0 and 1000")
	}
	rel, path, err := s.searchPath(in.Path)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	info, err := s.root.Stat(rel)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	args := []string{"--json", "--line-number", "--color=never", "--hidden", "--glob", "!.kaguya/tool-output/**"}
	if in.IgnoreCase {
		args = append(args, "--ignore-case")
	}
	if in.Literal {
		args = append(args, "--fixed-strings")
	}
	if in.Glob != "" {
		args = append(args, "--glob", in.Glob)
	}
	args = append(args, "--", in.Pattern, path)
	type match struct {
		path, text string
		line       int
	}
	matches := []match{}
	err = s.streamCommand(ctx, "rg", args, false, func(line string) (bool, error) {
		var event struct {
			Type string `json:"type"`
			Data struct {
				Path  rgText `json:"path"`
				Lines rgText `json:"lines"`
				Line  int    `json:"line_number"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return false, err
		}
		if event.Type != "match" {
			return false, nil
		}
		p, e := event.Data.Path.value()
		if e != nil {
			return false, e
		}
		text, e := event.Data.Lines.value()
		if e != nil {
			return false, e
		}
		matches = append(matches, match{p, text, event.Data.Line})
		return len(matches) > limit, nil
	})
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if len(matches) == 0 {
		return fantasy.NewTextResponse("No matches found"), nil
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	lines := []string{}
	longLines := false
	emit := func(p string, line int, text string, matching bool) {
		r := []rune(text)
		if len(r) > 500 {
			text = string(r[:500]) + "... [truncated]"
			longLines = true
		}
		separator := ":"
		if !matching {
			separator = "-"
		}
		lines = append(lines, fmt.Sprintf("%s%s%d%s %s", p, separator, line, separator, text))
	}
	for _, m := range matches {
		if ctx.Err() != nil {
			return fantasy.ToolResponse{}, ctx.Err()
		}
		display := filepath.Base(m.path)
		if info.IsDir() {
			display, err = filepath.Rel(path, m.path)
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
		}
		if in.Context == 0 {
			emit(filepath.ToSlash(display), m.line, strings.TrimSuffix(normalizeLF(m.text), "\n"), true)
			continue
		}
		p, err := s.resolve(m.path)
		if err != nil {
			return fantasy.ToolResponse{}, err
		}
		data, err := s.readBytes(ctx, p)
		if err != nil {
			return fantasy.ToolResponse{}, err
		}
		all := strings.Split(normalizeLF(string(data)), "\n")
		for i := max(1, m.line-in.Context); i <= min(len(all), m.line+in.Context); i++ {
			emit(filepath.ToSlash(display), i, all[i-1], i == m.line)
		}
		if len(strings.Join(lines, "\n")) > MaxBytes {
			break
		}
	}
	r := truncate(strings.Join(lines, "\n"), 100000, false)
	text := r.Content
	if more {
		text += fmt.Sprintf("\n\n[%d matches limit reached; increase limit or refine the pattern.]", limit)
	}
	if r.Truncated {
		text += "\n\n[50KB output limit reached.]"
	}
	if longLines {
		text += "\n\n[Some lines truncated to 500 characters; use read for full lines.]"
	}
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(text), map[string]any{"truncation": r, "matchLimitReached": more, "linesTruncated": longLines}), nil
}
