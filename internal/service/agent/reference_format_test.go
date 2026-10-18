package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lyonmu/kaguya/internal/agent/workspace"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	"github.com/lyonmu/kaguya/internal/ent/kaguyachatturn"
)

// formatter 的文件行为使用真实 Root/句柄；仅需要 DB 的集成沿用 references_test。
func newReferenceReaderForTest(t *testing.T, ctx context.Context, dir string, budget int64, refs []dtochat.Reference) *referenceReader {
	t.Helper()
	root, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	matcher, err := workspace.NewIgnoreMatcher(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	r := &referenceReader{ctx: ctx, scanCtx: ctx, filter: workspace.NewReferenceFilter(ctx, root, matcher), budget: budget, metadata: workspace.ReferenceMetadataBudget{Limit: referenceMetadataBytes}}
	for _, ref := range explicitFilesFirst(refs) {
		if err := r.addSource(ref); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestReferenceFormatterCompleteLinesAndActualHash(t *testing.T) {
	for _, ignoreAllowance := range []bool{false, true} {
		t.Run(fmt.Sprintf("seam_ignores_allowance=%t", ignoreAllowance), func(t *testing.T) {
			dir := t.TempDir()
			line := "引用证据💡-line-marker"
			body := strings.Repeat(line+"\n", 500)
			if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			r := newReferenceReaderForTest(t, context.Background(), dir, 2048, []dtochat.Reference{{Type: "file", Path: "note.txt"}})
			r.budgetKnown = true
			if ignoreAllowance {
				r.readWindow = func(ctx context.Context, file *os.File) (workspace.TextWindow, error) {
					return workspace.ReadTextWindowFile(ctx, file, 0, referenceLineLimit, referenceMaxFileBytes, referenceLineByteLimit, referenceLineByteLimit)
				}
			}
			result, err := r.format()
			if err != nil {
				t.Fatal(err)
			}
			if len(result.prompt) > 2048 || !utf8.ValidString(result.prompt) || result.summary.ScanIncomplete || result.summary.PartialReason != "context_budget" || result.summary.BudgetKnown == nil || !*result.summary.BudgetKnown {
				t.Fatalf("result=%+v bytes=%d", result.summary, len(result.prompt))
			}
			f := result.summary.Sources[0].Files[0]
			if !f.Included || len(f.Ranges) != 1 || f.Ranges[0].Start != 1 || f.Ranges[0].End <= 0 || f.Ranges[0].End >= 500 || f.NextLine != f.Ranges[0].End+1 {
				t.Fatalf("file=%+v", f)
			}
			prefix := strings.TrimSuffix(strings.Repeat(line+"\n", f.Ranges[0].End), "\n")
			if f.Hash != fmt.Sprintf("%x", sha256.Sum256([]byte(prefix))) || !strings.Contains(result.prompt, prefix+"\nUse workspace") || strings.Count(result.prompt, line) != f.Ranges[0].End {
				t.Fatalf("hash/range do not describe actual excerpt: %+v", f)
			}
			if r.sources[0].entries[0].text != "" || r.metadata.Used < 0 || r.metadata.Used > referenceMetadataBytes {
				t.Fatalf("retained body/metadata: %+v used=%d", r.sources[0].entries[0], r.metadata.Used)
			}
			encoded, err := json.Marshal(result.summary)
			if err != nil || len(encoded) > referenceMetadataBytes || result.summary.InlineBytes != int64(len(result.prompt)) {
				t.Fatalf("summary bytes=%d err=%v", len(encoded), err)
			}
		})
	}
}

func TestReferenceFormatterMetadataOnlyAndEmptyLogicalLine(t *testing.T) {
	cases := []struct {
		name, body, status string
		included           bool
		large              bool
	}{
		{name: "empty", status: "empty"},
		{name: "binary", body: "\x00binary", status: "binary"},
		{name: "invalid_utf8", body: "\xff", status: "binary"},
		{name: "oversized", body: strings.Repeat("x", referenceLineByteLimit+1), status: "oversized_line"},
		{name: "too_large", status: "too_large", large: true},
		{name: "empty_logical_line", body: "\n", status: "included", included: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			filename := filepath.Join(dir, "note.txt")
			if err := os.WriteFile(filename, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.large {
				if err := os.Truncate(filename, referenceMaxFileBytes+1); err != nil {
					t.Fatal(err)
				}
			}
			r := newReferenceReaderForTest(t, context.Background(), dir, 2048, []dtochat.Reference{{Type: "file", Path: "note.txt"}})
			result, err := r.format()
			if err != nil {
				t.Fatal(err)
			}
			f := result.summary.Sources[0].Files[0]
			if f.Status != tc.status || f.Included != tc.included || result.summary.BudgetKnown == nil || *result.summary.BudgetKnown {
				t.Fatalf("file=%+v summary=%+v", f, result.summary)
			}
			if tc.included {
				if len(f.Ranges) != 1 || f.Ranges[0].End != 1 || f.Hash != fmt.Sprintf("%x", sha256.Sum256(nil)) {
					t.Fatalf("empty logical line=%+v", f)
				}
			} else if f.Hash != "" || len(f.Ranges) != 0 || f.NextLine != 0 {
				t.Fatalf("fabricated content=%+v", f)
			}
		})
	}
}

func TestReferenceFormatterAllBinaryPartialIsNotCapacityFailure(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 100; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("binary-%03d", i)), []byte("\x00binary"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r := newReferenceReaderForTest(t, context.Background(), dir, 1024, []dtochat.Reference{{Type: "directory", Path: "."}})
	result, err := r.format()
	if err != nil {
		t.Fatal(err)
	}
	if !result.summary.ScanIncomplete || result.summary.Sources[0].Included || len(result.summary.Sources[0].Files) == 0 {
		t.Fatalf("summary=%+v", result.summary)
	}
	for _, file := range result.summary.Sources[0].Files {
		if file.Status != "binary" || file.Included || file.Hash != "" || len(file.Ranges) != 0 {
			t.Fatalf("file=%+v", file)
		}
	}
}

func TestReferenceFormatterBudgetBoundariesAndUnfittableLine(t *testing.T) {
	dir := t.TempDir()
	// 整行比任何可用窗口都大：不能输出半行，也不能伪报 empty/binary。
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte(strings.Repeat("x", 4000)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, budget := range []int64{0, 1, 1023, 1024, 4096} {
		t.Run(fmt.Sprintf("budget_%d", budget), func(t *testing.T) {
			r := newReferenceReaderForTest(t, context.Background(), dir, budget, []dtochat.Reference{{Type: "file", Path: "note.txt"}})
			result, err := r.format()
			if result != nil || !errors.Is(err, ErrContextBudgetExceeded) {
				t.Fatalf("budget=%d result=%+v err=%v", budget, result, err)
			}
		})
	}
	// 上限未知仍保留有限预算；大窗口也不突破旧50KiB/2000行窗口。
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte(strings.Repeat("line-marker\n", 30000)), 0600); err != nil {
		t.Fatal(err)
	}
	r := newReferenceReaderForTest(t, context.Background(), dir, referenceUnknownBytes, []dtochat.Reference{{Type: "file", Path: "note.txt"}})
	result, err := r.format()
	if err != nil || result == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	f := result.summary.Sources[0].Files[0]
	if len(result.prompt) > referenceUnknownBytes || *result.summary.BudgetKnown || f.Ranges[0].End != referenceLineLimit || f.NextLine != referenceLineLimit+1 || strings.Count(result.prompt, "line-marker") != referenceLineLimit {
		t.Fatalf("unknown budget/window: %+v", result.summary)
	}
}

func TestReferenceFormatterBodyPartialIsNotUnscannedDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte(strings.Repeat("line-marker\n", 500)), 0600); err != nil {
		t.Fatal(err)
	}
	r := newReferenceReaderForTest(t, context.Background(), dir, 2048, []dtochat.Reference{{Type: "directory", Path: "."}})
	result, err := r.format()
	if err != nil {
		t.Fatal(err)
	}
	if result.summary.ScanIncomplete || result.summary.PartialReason != "context_budget" || len(result.summary.Continuation) != 1 || len(result.summary.Continuation[0].UnscannedSubtrees) != 0 || result.summary.Continuation[0].KnownOmittedFiles != 0 {
		t.Fatalf("body partial mislabeled as unscanned: %+v", result.summary)
	}
}

func TestReferenceFormatterEscapedPaths(t *testing.T) {
	dir := t.TempDir()
	name := "report\"\nspoof-heading<script>.txt"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("path-safe-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	r := newReferenceReaderForTest(t, context.Background(), dir, 4096, []dtochat.Reference{{Type: "file", Path: name}})
	result, err := r.format()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.prompt, "\nspoof-heading") || !strings.Contains(result.prompt, fmt.Sprintf("%q", name)) || result.summary.Sources[0].Path != name || len(result.prompt) > 4096 {
		t.Fatalf("path injection or accounting: %s", result.prompt)
	}
}

func TestReferenceFormatterParentCancellationAfterRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("cancel-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newReferenceReaderForTest(t, ctx, dir, 4096, []dtochat.Reference{{Type: "directory", Path: "."}})
	r.readWindow = func(ctx context.Context, file *os.File) (workspace.TextWindow, error) {
		window, err := workspace.ReadTextWindowFile(ctx, file, 0, referenceLineLimit, referenceMaxFileBytes, referenceLineByteLimit, referenceLineByteLimit)
		cancel()
		return window, err
	}
	result, err := r.format()
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestReferenceFormatterLocalDeadlineDoesNotCompleteExplicitFile(t *testing.T) {
	for _, kind := range []string{"file", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("deadline-marker"), 0600); err != nil {
				t.Fatal(err)
			}
			name := "."
			if kind == "file" {
				name = "note.txt"
			}
			r := newReferenceReaderForTest(t, context.Background(), dir, 4096, []dtochat.Reference{{Type: kind, Path: name}})
			scanCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			r.scanCtx = scanCtx
			// filter 也必须使用相同的局部期限，不能只有 formatter 在检查。
			root, err := workspace.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			matcher, err := workspace.NewIgnoreMatcher(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			r.filter = workspace.NewReferenceFilter(scanCtx, root, matcher)
			result, err := r.format()
			if kind == "file" {
				if result != nil || !errors.Is(err, ErrReferenceReadFailure) {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				return
			}
			if err != nil || result == nil || !result.summary.ScanIncomplete || result.summary.PartialReason != "deadline" || result.summary.Sources[0].Included {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestReferenceFormatterStrictIgnoreErrorCannotBecomeWarning(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("ignore-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	r := newReferenceReaderForTest(t, context.Background(), dir, 4096, []dtochat.Reference{{Type: "directory", Path: "."}})
	r.readWindow = func(context.Context, *os.File) (workspace.TextWindow, error) {
		return workspace.TextWindow{}, workspace.ErrReferenceIgnore
	}
	result, err := r.format()
	if result != nil || !errors.Is(err, ErrReferenceReadFailure) || !errors.Is(err, workspace.ErrReferenceIgnore) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestReferenceFormatterAliasBindingChangedBeforeBody(t *testing.T) {
	for _, replaceTarget := range []bool{false, true} {
		t.Run(fmt.Sprintf("replace_same_target=%t", replaceTarget), func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"first.txt", "other.txt"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			alias := filepath.Join(dir, "alias.txt")
			if err := os.Symlink("first.txt", alias); err != nil {
				t.Fatal(err)
			}
			r := newReferenceReaderForTest(t, context.Background(), dir, 4096, []dtochat.Reference{{Type: "file", Path: "alias.txt"}})
			if replaceTarget {
				if err := os.Rename(filepath.Join(dir, "other.txt"), filepath.Join(dir, "first.txt")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(alias); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("other.txt", alias); err != nil {
					t.Fatal(err)
				}
			}
			reads := 0
			r.readWindow = func(context.Context, *os.File) (workspace.TextWindow, error) {
				reads++
				return workspace.TextWindow{}, nil
			}
			result, err := r.format()
			if result != nil || !errors.Is(err, ErrReferenceReadFailure) || reads != 0 {
				t.Fatalf("result=%+v err=%v reads=%d", result, err, reads)
			}
		})
	}
}

func TestFailedTurnPreservesActualReferenceAuditAndBinding(t *testing.T) {
	ctx, client := setupChatTest(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("failed-reference-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	r := newReferenceReaderForTest(t, ctx, dir, 2048, []dtochat.Reference{{Type: "file", Path: "note.txt"}})
	result, err := r.format()
	if err != nil {
		t.Fatal(err)
	}
	row, err := beginTurn(ctx, turnStart{ConversationID: "failed-reference-fixture", UserContent: "inspect", StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := markTurnEndWithPreparation(row.ID, "other-conversation", kaguyachatturn.StatusFailed, time.Now(), nil, result.summary); !errors.Is(err, ErrConversationBusy) {
		t.Fatalf("binding err=%v", err)
	}
	if err := markTurnEndWithPreparation(row.ID, row.ConversationID, kaguyachatturn.StatusFailed, time.Now(), nil, result.summary); err != nil {
		t.Fatal(err)
	}
	stored, err := client.KaguyaChatTurn.Get(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := json.Marshal(result.summary)
	actual, _ := json.Marshal(stored.ReferenceSummary)
	if stored.Status != kaguyachatturn.StatusFailed || string(actual) != string(expected) || stored.UserContent != "inspect" || len(stored.ContextMessages) != 0 {
		t.Fatalf("failed audit=%s status=%s", actual, stored.Status)
	}
	other := &dtochat.TurnReferenceSummary{Sources: []dtochat.TurnReferenceSource{{Type: "file", Path: "replacement.txt"}}}
	if err := markTurnEndWithPreparation(row.ID, row.ConversationID, kaguyachatturn.StatusFailed, time.Now(), nil, other); err != nil {
		t.Fatal(err)
	}
	if _, err := client.KaguyaChatTurn.UpdateOneID(row.ID).SetStatus(kaguyachatturn.StatusCompleted).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := markTurnEndWithPreparation(row.ID, row.ConversationID, kaguyachatturn.StatusFailed, time.Now(), nil, other); !errors.Is(err, ErrConversationBusy) {
		t.Fatalf("completed overwrite err=%v", err)
	}
	stored, err = client.KaguyaChatTurn.Get(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	actual, _ = json.Marshal(stored.ReferenceSummary)
	if stored.Status != kaguyachatturn.StatusCompleted || string(actual) != string(expected) {
		t.Fatalf("terminal overwritten: %s", actual)
	}
}
