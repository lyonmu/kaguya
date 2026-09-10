package tools

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/fantasy"
	"golang.org/x/text/unicode/norm"
)

type Replacement struct {
	OldText string `json:"oldText" description:"Non-empty text copied from the current file. Include enough surrounding text to match exactly once; do not include displayed line numbers."`
	NewText string `json:"newText" description:"Complete replacement for oldText. Required even when empty: use an empty string to delete the matched text."`
}
type EditInput struct {
	Path  string        `json:"path" description:"One existing UTF-8 file inside the project; prefer a relative path. Symlink mutation paths are rejected."`
	Edits []Replacement `json:"edits" description:"Non-empty array of {oldText,newText} objects. All match the same original file; a later entry cannot match text inserted by an earlier entry."`
}
type WriteInput struct {
	Path    string `json:"path" description:"Destination file inside the project; parent directories are created. Existing files are fully overwritten; symlink mutation paths are rejected."`
	Content string `json:"content" description:"Entire UTF-8 file content, not a patch or fragment. Required; an empty string intentionally creates or truncates an empty file."`
}

func (s *Set) EditTool() fantasy.AgentTool {
	return tool(s, "edit", `Make targeted replacements in one existing file after reading the relevant lines. Supply path and edits (an array), not a unified diff, old_string/new_string, or top-level oldText/newText. Each oldText must be non-empty and match exactly once in the ORIGINAL file; all entries are checked before any write. Merge overlapping changes into one replacement. If a match is missing or ambiguous, reread the affected range and add context before retrying. newText may be empty to delete. BOM and line endings are preserved. Example: {"path":"src/main.go","edits":[{"oldText":"const retries = 2","newText":"const retries = 3"}]}.`, s.edit)
}
func (s *Set) WriteTool() fantasy.AgentTool {
	return tool(s, "write", `Create a new UTF-8 file or replace an existing file's ENTIRE content. This is not append and does not accept a patch; prefer edit for targeted changes to an existing file. Parent directories are created automatically. Both path and content are required; content may be empty to intentionally truncate the file. Example: {"path":"notes.txt","content":"First line\nSecond line\n"}. Paths must stay inside the project; symlink mutation paths are rejected.`, s.write)
}
func normalizeLF(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}
func fuzzy(text string) string {
	lines := strings.Split(norm.NFKC.String(text), "\n")
	for i := range lines {
		lines[i] = strings.TrimRightFunc(lines[i], unicode.IsSpace)
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '\u2018', '\u2019', '\u201a', '\u201b':
			return '\''
		case '\u201c', '\u201d', '\u201e', '\u201f':
			return '"'
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
			return '-'
		}
		if r == '\u00a0' || r >= '\u2002' && r <= '\u200a' || r == '\u202f' || r == '\u205f' || r == '\u3000' {
			return ' '
		}
		return r
	}, strings.Join(lines, "\n"))
}

type matchedEdit struct {
	start, end, index int
	text              string
}

func applyEdits(original string, edits []Replacement) (string, int, error) {
	if len(edits) == 0 {
		return "", 0, errors.New("edits must contain at least one replacement")
	}
	base := original
	useFuzzy := false
	for i, e := range edits {
		if e.OldText == "" {
			return "", 0, fmt.Errorf("edits[%d].oldText must not be empty", i)
		}
		if !strings.Contains(original, normalizeLF(e.OldText)) {
			useFuzzy = true
		}
	}
	if useFuzzy {
		base = fuzzy(original)
	}
	matches := make([]matchedEdit, 0, len(edits))
	for i, e := range edits {
		old := normalizeLF(e.OldText)
		if !strings.Contains(base, old) {
			old = fuzzy(old)
		}
		if old == "" {
			return "", 0, fmt.Errorf("edits[%d].oldText is empty after normalization", i)
		}
		start := strings.Index(base, old)
		if start < 0 {
			return "", 0, fmt.Errorf("could not find edits[%d]; oldText must match the original file", i)
		}
		// As in pi, fuzzy-equivalent duplicates are ambiguous even with one exact match.
		if strings.Count(fuzzy(base), fuzzy(old)) > 1 || strings.Contains(base[start+1:], old) {
			return "", 0, fmt.Errorf("edits[%d] is not unique; provide more context", i)
		}
		matches = append(matches, matchedEdit{start: start, end: start + len(old), index: i, text: normalizeLF(e.NewText)})
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].start < matches[j].start })
	for i := 1; i < len(matches); i++ {
		if matches[i].start < matches[i-1].end {
			return "", 0, fmt.Errorf("edits[%d] and edits[%d] overlap; merge them", matches[i-1].index, matches[i].index)
		}
	}
	replace := func(text string, ms []matchedEdit, offset int) string {
		for i := len(ms) - 1; i >= 0; i-- {
			m := ms[i]
			text = text[:m.start-offset] + m.text + text[m.end-offset:]
		}
		return text
	}
	result := replace(base, matches, 0)
	if useFuzzy {
		// Rewrite only touched normalized line blocks, retaining untouched original bytes.
		oldLines := strings.SplitAfter(original, "\n")
		baseLines := strings.SplitAfter(base, "\n")
		if len(oldLines) != len(baseLines) {
			return "", 0, errors.New("normalization changed line count")
		}
		offsets := make([]int, len(baseLines)+1)
		for i, line := range baseLines {
			offsets[i+1] = offsets[i] + len(line)
		}
		var out strings.Builder
		nextLine := 0
		for i := 0; i < len(matches); {
			first := sort.Search(len(baseLines), func(n int) bool { return offsets[n+1] > matches[i].start })
			last := sort.Search(len(baseLines), func(n int) bool { return offsets[n+1] >= matches[i].end }) + 1
			j := i + 1
			for j < len(matches) && matches[j].start < offsets[last] {
				last = sort.Search(len(baseLines), func(n int) bool { return offsets[n+1] >= matches[j].end }) + 1
				j++
			}
			out.WriteString(strings.Join(oldLines[nextLine:first], ""))
			out.WriteString(replace(base[offsets[first]:offsets[last]], matches[i:j], offsets[first]))
			nextLine = last
			i = j
		}
		out.WriteString(strings.Join(oldLines[nextLine:], ""))
		result = out.String()
	}
	if result == original {
		return "", 0, errors.New("no changes made: replacements produced identical content")
	}
	first := 0
	for first < len(original) && first < len(result) && original[first] == result[first] {
		first++
	}
	return result, strings.Count(result[:first], "\n") + 1, nil
}
func (s *Set) edit(ctx context.Context, in EditInput) (fantasy.ToolResponse, error) {
	path, err := s.resolve(in.Path)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	key, err := s.mutationPath(path)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	release, err := lockPath(ctx, key)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	defer release()
	data, err := s.readBytes(ctx, path)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return fantasy.ToolResponse{}, errors.New("edit requires UTF-8 text")
	}
	original := string(data)
	bom := ""
	if strings.HasPrefix(original, "\ufeff") {
		bom = "\ufeff"
		original = strings.TrimPrefix(original, bom)
	}
	crlf := strings.Index(original, "\r\n")
	lf := strings.Index(original, "\n")
	endingCRLF := crlf >= 0 && (lf < 0 || crlf < lf)
	base := normalizeLF(original)
	updated, line, err := applyEdits(base, in.Edits)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	final := updated
	if endingCRLF {
		final = strings.ReplaceAll(final, "\n", "\r\n")
	}
	if err := s.atomicWrite(ctx, path, []byte(bom+final)); err != nil {
		return fantasy.ToolResponse{}, err
	}
	patch := unifiedPatch(in.Path, base, updated)
	diff := truncate(patch, MaxLines, false)
	details := map[string]any{"diff": diff.Content, "firstChangedLine": line, "truncated": diff.Truncated}
	if !diff.Truncated {
		details["patch"] = patch
	}
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(fmt.Sprintf("Successfully replaced %d block(s) in %s.\n%s", len(in.Edits), in.Path, diff.Content)), details), nil
}
func (s *Set) write(ctx context.Context, in WriteInput) (fantasy.ToolResponse, error) {
	path, err := s.resolve(in.Path)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	key, err := s.mutationPath(path)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	release, err := lockPath(ctx, key)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	defer release()
	if err := s.atomicWrite(ctx, path, []byte(in.Content)); err != nil {
		return fantasy.ToolResponse{}, err
	}
	return fantasy.NewTextResponse("Successfully wrote to " + in.Path), nil
}
func (s *Set) atomicWrite(ctx context.Context, path string, data []byte) error {
	if len(data) > MaxFileBytes {
		return errors.New("write exceeds 32MB safety limit")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := s.mutationPath(path); err != nil {
		return err
	}
	mode := os.FileMode(0644)
	info, err := s.root.Stat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("target must be a regular file")
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := s.root.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), ".kaguya-write-"+rand.Text())
	f, err := s.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer s.root.Remove(tmp)
	if info != nil {
		if err := f.Chmod(mode); err != nil {
			f.Close()
			return err
		}
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.root.Rename(tmp, path)
}

// A standard unified hunk, narrowed by shared prefix/suffix. No quadratic diff
// algorithm or unbounded LCS matrix is needed for large generated files.
func unifiedPatch(path, old, new string) string {
	a, b := strings.SplitAfter(old, "\n"), strings.SplitAfter(new, "\n")
	if a[len(a)-1] == "" {
		a = a[:len(a)-1]
	}
	if b[len(b)-1] == "" {
		b = b[:len(b)-1]
	}
	first := 0
	for first < len(a) && first < len(b) && a[first] == b[first] {
		first++
	}
	first = max(0, first-4)
	ae, be := len(a), len(b)
	for ae > first && be > first && a[ae-1] == b[be-1] {
		ae--
		be--
	}
	ae = min(len(a), ae+4)
	be = min(len(b), be+4)
	var out strings.Builder
	oldStart, newStart := first+1, first+1
	if ae == first {
		oldStart = first
	}
	if be == first {
		newStart = first
	}
	fmt.Fprintf(&out, "--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n", path, path, oldStart, ae-first, newStart, be-first)
	emit := func(prefix string, lines []string) {
		for _, line := range lines {
			out.WriteString(prefix)
			out.WriteString(line)
			if !strings.HasSuffix(line, "\n") {
				out.WriteString("\n\\ No newline at end of file\n")
			}
		}
	}
	emit("-", a[first:ae])
	emit("+", b[first:be])
	return out.String()
}
