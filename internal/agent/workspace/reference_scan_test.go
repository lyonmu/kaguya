package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestReferenceScanUsesBytesNotFileCount(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2100; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f-%04d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	root := newRoot(t, dir)
	matcher, err := NewIgnoreMatcher(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	filter := NewReferenceFilter(context.Background(), root, matcher)
	budget := ReferenceMetadataBudget{Limit: 64 << 10}
	count := 0
	scan, err := filter.WalkReferences(".", &budget, func(kind, name string, _ os.FileInfo) error {
		if !budget.Take(int64(len(name) + 2)) {
			return ErrReferenceMetadataBudget
		}
		count++
		return nil
	})
	if err != nil || scan.Incomplete || count != 2100 {
		t.Fatalf("scan=%+v count=%d err=%v", scan, count, err)
	}
	if budget.Used > budget.Limit {
		t.Fatalf("metadata=%d", budget.Used)
	}
	budget = ReferenceMetadataBudget{Limit: 200}
	count = 0
	scan, err = filter.WalkReferences(".", &budget, func(kind, name string, _ os.FileInfo) error {
		if !budget.Take(int64(len(name) + 2)) {
			return ErrReferenceMetadataBudget
		}
		count++
		return nil
	})
	if err != nil || !scan.Incomplete || scan.Reason != "metadata_budget" || count >= 2100 || budget.Used > budget.Limit {
		t.Fatalf("scan=%+v count=%d used=%d err=%v", scan, count, budget.Used, err)
	}
}

func TestReferenceScanCancellationIsAnError(t *testing.T) {
	dir, root, _ := referenceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	matcher, err := NewIgnoreMatcher(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	filter := NewReferenceFilter(ctx, root, matcher)
	budget := ReferenceMetadataBudget{Limit: 64 << 10}
	visits := 0
	_, err = filter.WalkReferences(".", &budget, func(kind, name string, _ os.FileInfo) error {
		visits++
		cancel()
		return nil
	})
	if visits != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("visits=%d err=%v (%s)", visits, err, dir)
	}
}

func TestReferenceFilterRejectsLenientPolicy(t *testing.T) {
	_, root, _ := referenceFixture(t)
	for _, matcher := range []*IgnoreMatcher{nil, NewLenientIgnoreMatcher(context.Background(), root)} {
		filter := NewReferenceFilter(context.Background(), root, matcher)
		if _, err := filter.Check("visible.txt", "file", false); !errors.Is(err, ErrReferenceIgnore) {
			t.Fatal(err)
		}
	}
}

func TestReferenceScanChangedChildDoesNotDiscardOtherFiles(t *testing.T) {
	_, root, filter := referenceFixture(t)
	budget := ReferenceMetadataBudget{Limit: 64 << 10}
	safeSeen := false
	scan, err := filter.WalkReferences(".", &budget, func(kind, name string, _ os.FileInfo) error {
		if name == "sub" && kind == "directory" {
			if err := root.RemoveAll("sub"); err != nil {
				t.Fatal(err)
			}
		}
		if name == "visible.txt" {
			safeSeen = true
		}
		return nil
	})
	if err != nil || !safeSeen || len(scan.Warnings) != 1 || scan.Warnings[0] != "\"sub\": directory_unavailable" {
		t.Fatalf("scan=%+v safe=%v err=%v", scan, safeSeen, err)
	}
}
