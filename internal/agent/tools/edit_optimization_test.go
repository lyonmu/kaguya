package tools

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// Frozen 4f2a434 matching algorithm: the reference deliberately repeats normalization.
func referenceApplyEdits(original string, edits []Replacement) (string, int, error) {
	if len(edits) == 0 {
		return "", 0, fmt.Errorf("edits must contain at least one replacement")
	}
	base, useFuzzy := original, false
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
		oldLines, baseLines := strings.SplitAfter(original, "\n"), strings.SplitAfter(base, "\n")
		if len(oldLines) != len(baseLines) {
			return "", 0, fmt.Errorf("normalization changed line count")
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
		return "", 0, fmt.Errorf("no changes made: replacements produced identical content")
	}
	first := 0
	for first < len(original) && first < len(result) && original[first] == result[first] {
		first++
	}
	return result, strings.Count(result[:first], "\n") + 1, nil
}

func TestApplyEditsMatchesUncachedReference(t *testing.T) {
	for _, tc := range []struct {
		original string
		edits    []Replacement
	}{
		{"a\nb\nc", []Replacement{{"a", "A"}, {"c", "C"}}},
		{"keep  \n“a”\nＢ\nend\t", []Replacement{{"\"a\"", "one"}, {"Ｂ", "two"}}},
		{"a\nａ", []Replacement{{"a", "A"}}},
		{"abc", []Replacement{{"ab", "A"}, {"bc", "B"}}},
		{"abc", []Replacement{{"missing", "x"}, {"", "x"}}},
		{"abc", []Replacement{{" ", "x"}}},
		{"abc", []Replacement{{"a", "a"}}},
		{"abc", nil},
		{"x\n", []Replacement{{"x\r\n", "y\r\n"}}},
	} {
		got, line, err := applyEdits(tc.original, tc.edits)
		want, wantLine, wantErr := referenceApplyEdits(tc.original, tc.edits)
		if got != want || line != wantLine || fmt.Sprint(err) != fmt.Sprint(wantErr) {
			t.Fatalf("%q %+v: got %q/%d/%v want %q/%d/%v", tc.original, tc.edits, got, line, err, want, wantLine, wantErr)
		}
	}
}

func BenchmarkEditNormalizationComparison(b *testing.B) {
	var text strings.Builder
	edits := make([]Replacement, 64)
	for i := range edits {
		line := fmt.Sprintf("line-%03d: 内容 — %s\n", i, strings.Repeat("x", 1000))
		text.WriteString(line)
		edits[i] = Replacement{line, fmt.Sprintf("updated-%d\n", i)}
	}
	original := text.String()
	for _, tc := range []struct {
		name  string
		apply func(string, []Replacement) (string, int, error)
	}{{"baseline", referenceApplyEdits}, {"cached", applyEdits}} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := tc.apply(original, edits); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
