package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type instructionTestEntry string

func (e instructionTestEntry) Name() string    { return string(e) }
func (instructionTestEntry) IsDir() bool       { return false }
func (instructionTestEntry) Type() fs.FileMode { return 0 }
func (instructionTestEntry) Info() (fs.FileInfo, error) {
	return nil, errors.New("unused test entry info")
}

func TestProjectInstructionDiscoveryBatchesAndCancellation(t *testing.T) {
	const total = 100000
	visited, calls := 0, 0
	names, err := projectInstructionNames(context.Background(), func(limit int) ([]os.DirEntry, error) {
		calls++
		if limit != 128 {
			t.Fatalf("unbounded directory batch: %d", limit)
		}
		var entries []os.DirEntry
		for visited < total && len(entries) < limit {
			name := fmt.Sprintf("unrelated-%06d", visited)
			if visited == 0 {
				name = "agents.md"
			} else if visited == total-1 {
				name = "AGENTS.md"
			}
			entries = append(entries, instructionTestEntry(name))
			visited++
		}
		if visited == total {
			return entries, io.EOF
		}
		return entries, nil
	})
	if err != nil || strings.Join(names, ",") != "AGENTS.md,agents.md" || calls != (total+127)/128 {
		t.Fatalf("names=%v calls=%d err=%v", names, calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls = 0
	names, err = projectInstructionNames(ctx, func(limit int) ([]os.DirEntry, error) {
		calls++
		cancel()
		return []os.DirEntry{instructionTestEntry("AGENTS.md")}, nil
	})
	if !errors.Is(err, context.Canceled) || names != nil || calls != 1 {
		t.Fatalf("cancel returned partial names=%v calls=%d err=%v", names, calls, err)
	}
}

type cancelInstructionRead struct {
	reader io.Reader
	cancel context.CancelFunc
	calls  int
}

func (r *cancelInstructionRead) Read(buf []byte) (int, error) {
	r.calls++
	n, err := r.reader.Read(buf)
	r.cancel()
	return n, err
}

func TestProjectInstructionReadChecksCancellationBetweenChunks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &cancelInstructionRead{reader: strings.NewReader(strings.Repeat("x", 256*1024)), cancel: cancel}
	data, err := readInstructionData(ctx, reader, 256*1024+1)
	if !errors.Is(err, context.Canceled) || data != nil || reader.calls != 1 {
		t.Fatalf("cancel returned partial bytes=%d calls=%d err=%v", len(data), reader.calls, err)
	}
	if text, err := loadProjectInstructions(ctx, t.TempDir()); !errors.Is(err, context.Canceled) || text != "" {
		t.Fatalf("canceled loader: len=%d err=%v", len(text), err)
	}
}

func TestProjectInstructionReadRejectsChangesAndClosesFile(t *testing.T) {
	for _, scenario := range []string{"grow", "replace", "delete", "fifo", "escape", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "AGENTS.md")
			if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var captured *os.File
			reader := instructionReader{readData: func(ctx context.Context, input io.Reader, limit int) ([]byte, error) {
				captured = input.(*os.File)
				data, err := readInstructionData(ctx, input, limit)
				if err != nil {
					return nil, err
				}
				switch scenario {
				case "grow":
					err = os.WriteFile(path, []byte("a different, larger file"), 0600)
				case "replace":
					temp := filepath.Join(dir, "new")
					if err = os.WriteFile(temp, []byte("replaced"), 0600); err == nil {
						err = os.Rename(temp, path)
					}
				case "delete":
					err = os.Remove(path)
				case "fifo":
					if err = os.Remove(path); err == nil {
						err = syscall.Mkfifo(path, 0600)
					}
				case "escape":
					outside := filepath.Join(t.TempDir(), "outside")
					if err = os.WriteFile(outside, []byte("outside"), 0600); err == nil {
						if err = os.Remove(path); err == nil {
							err = os.Symlink(outside, path)
						}
					}
				case "cancel":
					cancel()
				}
				return data, err
			}}
			err = reader.appendProject(ctx, root, dir)
			if err == nil || reader.out.Len() != 0 {
				t.Fatalf("changed candidate accepted: bytes=%d err=%v", reader.out.Len(), err)
			}
			if scenario == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if captured == nil {
				t.Fatal("reader was not exercised")
			}
			if _, err := captured.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("instruction handle was not closed: %v", err)
			}
		})
	}
}

func TestProjectInstructionBudgetIncludesHeaderAndIgnoresReferencePolicy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	header := fmt.Sprintf("\n\nProject instructions from %s:\n", path)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("AGENTS.md\n"), 0600); err != nil {
		t.Fatal(err)
	}
	const budget = 256 * 1024
	for _, extra := range []int{0, 1} {
		if err := os.WriteFile(path, []byte(strings.Repeat("x", budget-len(header)+extra)), 0600); err != nil {
			t.Fatal(err)
		}
		text, err := loadProjectInstructions(context.Background(), dir)
		if extra == 0 && (err != nil || len(text) != budget) {
			t.Fatalf("exact budget: bytes=%d err=%v", len(text), err)
		}
		if extra == 1 && (err == nil || text != "") {
			t.Fatalf("header exceeded budget: bytes=%d err=%v", len(text), err)
		}
	}
	if err := os.WriteFile(path, []byte{0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	if text, err := loadProjectInstructions(context.Background(), dir); err == nil || text != "" {
		t.Fatalf("invalid UTF-8 accepted: bytes=%d err=%v", len(text), err)
	}
}
