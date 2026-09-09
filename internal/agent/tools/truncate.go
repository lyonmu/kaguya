package tools

import (
	"strings"
	"unicode/utf8"
)

type Truncation struct {
	Content               string `json:"content"`
	Truncated             bool   `json:"truncated"`
	TruncatedBy           string `json:"truncatedBy,omitempty"`
	TotalLines            int    `json:"totalLines"`
	TotalBytes            int    `json:"totalBytes"`
	OutputLines           int    `json:"outputLines"`
	OutputBytes           int    `json:"outputBytes"`
	FirstLineExceedsLimit bool   `json:"firstLineExceedsLimit,omitempty"`
	LastLinePartial       bool   `json:"lastLinePartial,omitempty"`
}

func contentLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}
func truncate(text string, maxLines int, tail bool) Truncation {
	lines := contentLines(text)
	r := Truncation{Content: text, TotalLines: len(lines), TotalBytes: len(text), OutputLines: len(lines), OutputBytes: len(text)}
	if len(lines) <= maxLines && len(text) <= MaxBytes {
		return r
	}
	r.Truncated = true
	r.Content = ""
	r.OutputLines = 0
	r.OutputBytes = 0
	r.TruncatedBy = "lines"
	selected := make([]string, 0, min(maxLines, len(lines)))
	for i := 0; i < len(lines) && len(selected) < maxLines; i++ {
		idx := i
		if tail {
			idx = len(lines) - 1 - i
		}
		line := lines[idx]
		extra := len(line)
		if len(selected) > 0 {
			extra++
		}
		if r.OutputBytes+extra > MaxBytes {
			r.TruncatedBy = "bytes"
			if len(selected) == 0 {
				if tail {
					start := len(line) - MaxBytes
					for start < len(line) && !utf8.RuneStart(line[start]) {
						start++
					}
					selected = append(selected, line[start:])
					r.LastLinePartial = true
				} else {
					r.FirstLineExceedsLimit = true
				}
			}
			break
		}
		selected = append(selected, line)
		r.OutputBytes += extra
	}
	if tail {
		for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
			selected[i], selected[j] = selected[j], selected[i]
		}
	}
	r.Content = strings.Join(selected, "\n")
	r.OutputLines = len(selected)
	r.OutputBytes = len(r.Content)
	return r
}
