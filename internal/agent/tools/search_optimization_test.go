package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
)

// Reference formatting keeps the old Join at each context-group boundary.
func referenceGrepOutput(content string, contextLines, limit int) fantasy.ToolResponse {
	all := strings.Split(normalizeLF(content), "\n")
	var matches []int
	for i, line := range all {
		if strings.Contains(line, "HIT") {
			matches = append(matches, i+1)
		}
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	lines := []string{}
	long := false
	emit := func(line int, matching bool) {
		text := all[line-1]
		runes := []rune(text)
		if len(runes) > 500 {
			text = string(runes[:500]) + "... [truncated]"
			long = true
		}
		sep := "-"
		if matching {
			sep = ":"
		}
		lines = append(lines, fmt.Sprintf("a.txt%s%d%s %s", sep, line, sep, text))
	}
	for _, line := range matches {
		if contextLines == 0 {
			emit(line, true)
			continue
		}
		for i := max(1, line-contextLines); i <= min(len(all), line+contextLines); i++ {
			emit(i, i == line)
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
	if long {
		text += "\n\n[Some lines truncated to 500 characters; use read for full lines.]"
	}
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(text), map[string]any{"truncation": r, "matchLimitReached": more, "linesTruncated": long})
}

func TestGrepOutputMatchesJoinReference(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg unavailable")
	}
	s := setup(t)
	cases := []string{strings.Repeat("before\nHIT中文🙂\nafter\n", 150), strings.Repeat("HIT"+strings.Repeat("中", 600)+"\n", 120)}
	// Forty disjoint 3-line context groups end exactly at each byte boundary;
	// the following match detects accidental >= or changed comparison placement.
	for _, target := range []int{MaxBytes - 1, MaxBytes, MaxBytes + 1} {
		const n = 120
		prefixBytes := n - 1
		for i := 1; i <= n; i++ {
			prefixBytes += len(fmt.Sprintf("a.txt-%d- ", i))
		}
		remaining := target - prefixBytes
		var lines []string
		for i := range n {
			length := remaining / (n - i)
			remaining -= length
			line := strings.Repeat("x", length)
			if i%3 == 1 {
				line = "HIT" + line[3:]
			}
			lines = append(lines, line)
		}
		cases = append(cases, strings.Join(append(lines, "before", "HITextra", "after"), "\n"))
	}
	for index, content := range cases {
		if err := os.WriteFile(filepath.Join(s.cwd, "a.txt"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		for _, ctxLines := range []int{0, 1, 2, 10} {
			for _, limit := range []int{1, 40, 1000} {
				got, err := s.grep(context.Background(), GrepInput{Pattern: "HIT", Path: "a.txt", Context: ctxLines, Limit: &limit})
				if err != nil {
					t.Fatal(err)
				}
				want := referenceGrepOutput(content, ctxLines, limit)
				if got.Content != want.Content || got.Metadata != want.Metadata || got.IsError != want.IsError {
					t.Fatalf("case=%d context=%d limit=%d output or truncation metadata changed", index, ctxLines, limit)
				}
			}
		}
	}
}

func BenchmarkGrepCountingComparison(b *testing.B) {
	lines := make([]string, 120)
	for i := range lines {
		lines[i] = fmt.Sprintf("a.txt:%d: %s", i, strings.Repeat("中", 120))
	}
	for _, cached := range []bool{false, true} {
		name := "baseline"
		if cached {
			name = "cumulative"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				size := 0
				for i, line := range lines {
					if cached {
						if i > 0 {
							size++
						}
						size += len(line)
					} else {
						size = len(strings.Join(lines[:i+1], "\n"))
					}
					if size > MaxBytes {
						break
					}
				}
				if size != len(strings.Join(lines, "\n")) {
					b.Fatal("byte count mismatch")
				}
			}
		})
	}
}
