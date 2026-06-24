package files

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"charm.land/fantasy"
)

func (s *SafeFS) readFile(ctx context.Context, input ReadFileInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	abs, rel, err := s.resolve(input.Path)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	info, err := os.Stat(abs)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	if info.IsDir() {
		return fantasy.NewTextErrorResponse("path is a directory"), nil
	}

	if info.Size() > s.cfg.MaxFileBytes {
		return fantasy.NewTextErrorResponse("file is too large; use grep_files or narrow the target"), nil
	}

	if input.StartLine <= 0 {
		input.StartLine = 1
	}

	if input.Limit <= 0 {
		input.Limit = DefaultReadLines
	}

	if input.Limit > MaxReadLines {
		input.Limit = MaxReadLines
	}

	if ok, err := isTextFile(abs); err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	} else if !ok {
		return fantasy.NewTextErrorResponse("binary file is not readable by read_file"), nil
	}

	f, err := os.Open(abs)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var (
		lineNo  int
		lines   []string
		endLine int
		hasMore bool
	)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return fantasy.NewTextErrorResponse("read_file canceled"), nil
		default:
		}

		lineNo++

		if lineNo < input.StartLine {
			continue
		}

		if len(lines) >= input.Limit {
			hasMore = true
			break
		}

		lines = append(lines, scanner.Text())
		endLine = lineNo
	}

	if err := scanner.Err(); err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	result := ReadFileResult{
		Path:      rel,
		StartLine: input.StartLine,
		EndLine:   endLine,
		Limit:     input.Limit,
		HasMore:   hasMore,
		Lines:     lines,
	}

	if hasMore {
		result.NextStartLine = endLine + 1
	}

	return jsonResponse(result), nil
}

func (s *SafeFS) listFiles(ctx context.Context, input ListFilesInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	abs, _, err := s.resolve(input.Path)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	info, err := os.Stat(abs)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	if !info.IsDir() {
		return fantasy.NewTextErrorResponse("path is not a directory"), nil
	}

	if input.MaxEntries <= 0 {
		input.MaxEntries = DefaultMaxEntries
	}

	if input.MaxEntries > MaxEntries {
		input.MaxEntries = MaxEntries
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})

	var files []FileEntry
	truncated := false

	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return fantasy.NewTextErrorResponse("list_files canceled"), nil
		default:
		}

		name := entry.Name()

		if !input.IncludeHidden && strings.HasPrefix(name, ".") {
			continue
		}

		childAbs := filepath.Join(abs, name)

		realAbs, rel, err := s.resolve(filepath.ToSlash(mustRel(s.root, childAbs)))
		if err != nil {
			continue
		}

		stat, err := os.Stat(realAbs)
		if err != nil {
			continue
		}

		files = append(files, FileEntry{
			Name:    name,
			Path:    rel,
			IsDir:   stat.IsDir(),
			Size:    stat.Size(),
			ModTime: stat.ModTime().Format("2006-01-02T15:04:05Z07:00"),
		})

		if len(files) >= input.MaxEntries {
			truncated = true
			break
		}
	}

	return jsonResponse(map[string]any{
		"entries":   files,
		"count":     len(files),
		"truncated": truncated,
	}), nil
}

func mustRel(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return ""
	}
	return rel
}

func (s *SafeFS) grepFiles(ctx context.Context, input GrepFilesInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	if strings.TrimSpace(input.Query) == "" {
		return fantasy.NewTextErrorResponse("query is required"), nil
	}

	if len(input.Query) > 256 {
		return fantasy.NewTextErrorResponse("query is too long"), nil
	}

	abs, _, err := s.resolve(input.Path)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	if input.MaxMatches <= 0 {
		input.MaxMatches = DefaultMaxMatches
	}

	if input.MaxMatches > MaxMatches {
		input.MaxMatches = MaxMatches
	}

	var re *regexp.Regexp
	query := input.Query

	if input.UseRegex {
		pattern := query
		if !input.CaseSensitive {
			pattern = "(?i)" + pattern
		}

		re, err = regexp.Compile(pattern)
		if err != nil {
			return fantasy.NewTextErrorResponse("invalid regex: " + err.Error()), nil
		}
	} else if !input.CaseSensitive {
		query = strings.ToLower(query)
	}

	var matches []GrepMatch

	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		name := d.Name()

		if d.IsDir() {
			if _, denied := s.cfg.DenyDirs[name]; denied {
				return filepath.SkipDir
			}
			if !s.cfg.AllowHidden && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}

		if !s.cfg.AllowHidden && strings.HasPrefix(name, ".") {
			return nil
		}

		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return nil
		}

		rel = filepath.ToSlash(rel)

		if err := s.checkDenied(rel); err != nil {
			return nil
		}

		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() > s.cfg.MaxFileBytes {
			return nil
		}

		ok, err := isTextFile(path)
		if err != nil || !ok {
			return nil
		}

		fileMatches, err := grepOneFile(path, rel, query, re, input.CaseSensitive, input.MaxMatches-len(matches))
		if err != nil {
			return nil
		}

		matches = append(matches, fileMatches...)

		if len(matches) >= input.MaxMatches {
			return errors.New("match limit reached")
		}

		return nil
	})

	truncated := false
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return fantasy.NewTextErrorResponse("grep_files canceled"), nil
		}
		if err.Error() == "match limit reached" {
			truncated = true
		}
	}

	return jsonResponse(map[string]any{
		"matches":   matches,
		"count":     len(matches),
		"truncated": truncated,
	}), nil
}

func grepOneFile(path, rel, query string, re *regexp.Regexp, caseSensitive bool, limit int) ([]GrepMatch, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var matches []GrepMatch
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := scanner.Text()

		ok := false
		if re != nil {
			ok = re.MatchString(line)
		} else {
			target := line
			if !caseSensitive {
				target = strings.ToLower(target)
			}
			ok = strings.Contains(target, query)
		}

		if ok {
			matches = append(matches, GrepMatch{
				Path:    rel,
				Line:    lineNo,
				Content: line,
			})

			if len(matches) >= limit {
				break
			}
		}
	}

	return matches, scanner.Err()
}

func (s *SafeFS) fileMetadata(ctx context.Context, input FileMetadataInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	select {
	case <-ctx.Done():
		return fantasy.NewTextErrorResponse("file_metadata canceled"), nil
	default:
	}

	abs, rel, err := s.resolve(input.Path)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	info, err := os.Stat(abs)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}

	return jsonResponse(map[string]any{
		"path":     rel,
		"is_dir":   info.IsDir(),
		"size":     info.Size(),
		"mod_time": info.ModTime().Format("2006-01-02T15:04:05Z07:00"),
		"mode":     info.Mode().String(),
	}), nil
}
